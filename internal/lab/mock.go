package lab

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// MockProvider has its own durable database: it does not join the local order transaction.
// No payment, broker, account, credentials, or real-money integration exists.
type MockProvider struct {
	db    *sql.DB
	Delay time.Duration
}

func OpenMock(path string) (*MockProvider, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS attempts(id TEXT PRIMARY KEY, n INTEGER NOT NULL, scenario TEXT NOT NULL) STRICT;
CREATE TABLE IF NOT EXISTS effects(id TEXT PRIMARY KEY, amount_minor INTEGER NOT NULL, receipt TEXT NOT NULL) STRICT;
`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &MockProvider{db: db, Delay: time.Second}, nil
}

func (m *MockProvider) Close() error { return m.db.Close() }

type Evidence struct {
	Effects int `json:"effects"`
	Posts   int `json:"post_attempts"`
}

func (m *MockProvider) Evidence(id string) (Evidence, error) {
	var e Evidence
	err := m.db.QueryRow(`SELECT (SELECT count(*) FROM effects WHERE id=?), coalesce((SELECT n FROM attempts WHERE id=?),0)`, id, id).Scan(&e.Effects, &e.Posts)
	return e, err
}

func (m *MockProvider) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", m.place)
	mux.HandleFunc("GET /orders/{id}", m.lookup)
	return mux
}

func (m *MockProvider) lookup(w http.ResponseWriter, r *http.Request) {
	var scenario string
	err := m.db.QueryRow("SELECT scenario FROM attempts WHERE id=?", r.PathValue("id")).Scan(&scenario)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "storage_error", 500)
		return
	}
	if scenario == "unavailable" || scenario == "query_unavailable" {
		http.Error(w, "simulated_unavailable", 503)
		return
	}
	var p providerRecord
	err = m.db.QueryRow("SELECT id,amount_minor,receipt FROM effects WHERE id=?", r.PathValue("id")).Scan(&p.ID, &p.Amount, &p.Receipt)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "authoritative_not_found", 404)
		return
	}
	if err != nil {
		http.Error(w, "storage_error", 500)
		return
	}
	p.Status = "succeeded"
	respond(w, 200, p)
}

func (m *MockProvider) place(w http.ResponseWriter, r *http.Request) {
	var p providerRecord
	if err := decode(w, r, &p); err != nil || !tokenPattern.MatchString(p.ID) || p.Amount <= 0 || !validScenario(p.Scenario) || r.Header.Get("Idempotency-Key") != p.ID {
		http.Error(w, "invalid_input", 400)
		return
	}
	// Deliberately do not bind accepted work to request cancellation. A client
	// deadline does not revoke a downstream commit in a real distributed system.
	tx, err := m.db.Begin()
	if err != nil {
		http.Error(w, "storage_error", 500)
		return
	}
	defer tx.Rollback()
	var n int
	err = tx.QueryRow(`INSERT INTO attempts(id,n,scenario) VALUES(?,1,?) ON CONFLICT(id) DO UPDATE SET n=n+1 RETURNING n`, p.ID, p.Scenario).Scan(&n)
	if err != nil {
		http.Error(w, "storage_error", 500)
		return
	}
	if p.Scenario == "unavailable" || (p.Scenario == "transient" && n <= 2) {
		if err = tx.Commit(); err != nil {
			http.Error(w, "storage_error", 500)
			return
		}
		http.Error(w, "simulated_unavailable", 503)
		return
	}
	if p.Scenario == "reject" {
		if err = tx.Commit(); err != nil {
			http.Error(w, "storage_error", 500)
			return
		}
		p.Status = "rejected"
		respond(w, 422, p)
		return
	}
	p.Receipt = "sim_" + p.ID
	_, err = tx.Exec("INSERT INTO effects(id,amount_minor,receipt) VALUES(?,?,?) ON CONFLICT(id) DO NOTHING", p.ID, p.Amount, p.Receipt)
	if err != nil {
		http.Error(w, "storage_error", 500)
		return
	}
	var storedAmount int64
	err = tx.QueryRow("SELECT amount_minor,receipt FROM effects WHERE id=?", p.ID).Scan(&storedAmount, &p.Receipt)
	if err != nil {
		http.Error(w, "storage_error", 500)
		return
	}
	if storedAmount != p.Amount {
		http.Error(w, "idempotency_conflict", 409)
		return
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "storage_error", 500)
		return
	}
	if (p.Scenario == "commit_timeout" || p.Scenario == "query_unavailable") && n == 1 {
		time.Sleep(m.Delay)
	}
	if p.Scenario == "malformed" && n == 1 {
		respond(w, 200, map[string]string{"status": "maybe"})
		return
	}
	p.Status = "succeeded"
	respond(w, 200, p)
}
