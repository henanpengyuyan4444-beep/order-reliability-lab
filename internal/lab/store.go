// Learning extension written with AI assistance; see PROVENANCE.md.
package lab

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"maragu.dev/goqite"
)

//go:embed goqite.sql
var queueSchema string

const schema = `
CREATE TABLE orders (
 id TEXT PRIMARY KEY,
 idem_key TEXT NOT NULL UNIQUE,
 amount_minor INTEGER NOT NULL CHECK(amount_minor > 0),
 scenario TEXT NOT NULL,
 request_id TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','unknown','succeeded','rejected','needs_review')),
 attempts INTEGER NOT NULL DEFAULT 0,
 version INTEGER NOT NULL DEFAULT 0,
 receipt TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT ''
) STRICT;
`

var ErrConflict = errors.New("idempotency key already has different input")
var ErrInvalid = errors.New("invalid order input")
var ErrStale = errors.New("worker attempt superseded")
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Order struct {
	ID        string `json:"id"`
	Key       string `json:"idempotency_key"`
	Amount    int64  `json:"amount_minor"`
	Scenario  string `json:"scenario"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	Version   int    `json:"version"`
	Receipt   string `json:"receipt"`
	LastError string `json:"last_error"`
}

type Config struct {
	Lease       time.Duration
	RetryBase   time.Duration
	MaxAttempts int
}

func DefaultConfig() Config {
	return Config{Lease: 2 * time.Second, RetryBase: 250 * time.Millisecond, MaxAttempts: 5}
}

type Store struct {
	db    *sql.DB
	queue *goqite.Queue
	cfg   Config
	log   *slog.Logger
}

func openDB(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	u.RawQuery = "_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_foreign_keys=on&_txlock=immediate"
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	// One connection serializes short DB transactions, not downstream HTTP work.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Open(path string, cfg Config, logger *slog.Logger) (*Store, error) {
	if cfg.Lease <= 0 || cfg.RetryBase < 0 || cfg.MaxAttempts < 1 || cfg.MaxAttempts > 100 {
		return nil, errors.New("invalid worker configuration")
	}
	if logger == nil {
		logger = slog.Default()
	}
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return nil, err
	}
	defer tx.Rollback()
	var version int
	err = tx.QueryRow("PRAGMA user_version").Scan(&version)
	if err == nil && version == 0 {
		_, err = tx.Exec(schema + queueSchema + "PRAGMA user_version = 1;")
	}
	if err == nil && version > 1 {
		err = errors.New("unsupported schema version")
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, cfg: cfg, log: logger, queue: goqite.New(goqite.NewOpts{
		DB: db, Name: "orders", Timeout: cfg.Lease,
		// The application's persisted budget decides needs_review, including crashes.
		MaxReceive: 2147483647,
	})}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func newID() string { return fmt.Sprintf("%x", rand.Text()) }

func validScenario(s string) bool {
	switch s {
	case "normal", "commit_timeout", "transient", "unavailable", "reject", "malformed", "query_unavailable":
		return true
	}
	return false
}

const columns = "id, idem_key, amount_minor, scenario, request_id, status, attempts, version, receipt, last_error"

type scanner interface{ Scan(...any) error }

func scanOrder(row scanner) (Order, error) {
	var o Order
	err := row.Scan(&o.ID, &o.Key, &o.Amount, &o.Scenario, &o.RequestID, &o.Status, &o.Attempts, &o.Version, &o.Receipt, &o.LastError)
	return o, err
}

func (s *Store) Get(ctx context.Context, id string) (Order, error) {
	return scanOrder(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM orders WHERE id=?", id))
}

func (s *Store) Create(ctx context.Context, key string, amount int64, scenario, requestID string) (Order, bool, error) {
	if scenario == "" {
		scenario = "normal"
	}
	if !tokenPattern.MatchString(key) || !tokenPattern.MatchString(requestID) || amount <= 0 || amount > 1_000_000_000_000 || !validScenario(scenario) {
		return Order{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Order{}, false, err
	}
	defer tx.Rollback()
	o, err := scanOrder(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM orders WHERE idem_key=?", key))
	if err == nil {
		if o.Amount != amount || o.Scenario != scenario {
			return Order{}, false, ErrConflict
		}
		s.log.Info("order_replayed", "order_id", o.ID, "request_id", requestID, "original_request_id", o.RequestID)
		return o, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Order{}, false, err
	}
	o = Order{ID: newID(), Key: key, Amount: amount, Scenario: scenario, RequestID: requestID, Status: "pending"}
	_, err = tx.ExecContext(ctx, `INSERT INTO orders(id,idem_key,amount_minor,scenario,request_id,status) VALUES(?,?,?,?,?,?)`, o.ID, key, amount, scenario, requestID, o.Status)
	if err != nil {
		return Order{}, false, err
	}
	// No gap between committing an order and enqueueing its work.
	if err = s.queue.SendTx(ctx, tx, goqite.Message{Body: []byte(o.ID)}); err != nil {
		return Order{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Order{}, false, err
	}
	s.log.Info("order_created", "order_id", o.ID, "request_id", requestID)
	return o, true, nil
}

type claim struct {
	Order
	messageID goqite.ID
	first     bool
}

func terminal(status string) bool {
	return status == "succeeded" || status == "rejected" || status == "needs_review"
}

func (s *Store) claim(ctx context.Context) (*claim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	m, err := s.queue.ReceiveTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, tx.Commit()
	}
	o, err := scanOrder(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM orders WHERE id=?", string(m.Body)))
	if err != nil {
		return nil, err
	}
	if terminal(o.Status) || o.Attempts >= s.cfg.MaxAttempts {
		if !terminal(o.Status) {
			_, err = tx.ExecContext(ctx, "UPDATE orders SET status='needs_review',last_error='attempt_budget_exhausted' WHERE id=?", o.ID)
			if err != nil {
				return nil, err
			}
		}
		if err = s.queue.DeleteTx(ctx, tx, m.ID); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		s.log.Warn("order_budget_or_terminal", "order_id", o.ID, "request_id", o.RequestID, "attempt", o.Attempts)
		return nil, nil
	}
	first := o.Status == "pending"
	o.Attempts++
	o.Version++
	o.Status = "unknown"
	// Persist uncertainty BEFORE the external call: a crash may hide its result.
	_, err = tx.ExecContext(ctx, "UPDATE orders SET status='unknown', attempts=?,version=? WHERE id=?", o.Attempts, o.Version, o.ID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.log.Info("order_claimed", "order_id", o.ID, "request_id", o.RequestID, "attempt", o.Attempts, "version", o.Version)
	return &claim{Order: o, messageID: m.ID, first: first}, nil
}

func (s *Store) finish(ctx context.Context, c *claim, outcome Outcome) error {
	status := outcome.Status
	if status != "succeeded" && status != "rejected" {
		status = "unknown"
	}
	if status == "unknown" && c.Attempts >= s.cfg.MaxAttempts {
		status = "needs_review"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE orders SET status=?, receipt=?,last_error=? WHERE id=? AND version=? AND status='unknown'`, status, outcome.Receipt, outcome.Code, c.ID, c.Version)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrStale
	}
	if terminal(status) {
		err = s.queue.DeleteTx(ctx, tx, c.messageID)
	} else {
		delay := s.cfg.RetryBase * time.Duration(1<<min(c.Attempts-1, 5))
		err = s.queue.ExtendTx(ctx, tx, c.messageID, delay)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.log.Info("order_result", "order_id", c.ID, "request_id", c.RequestID, "attempt", c.Attempts, "status", status, "error_code", outcome.Code)
	return nil
}
