package lab

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maragu.dev/goqite"
)

func setup(t *testing.T) (*Store, *MockProvider, *Worker) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{Lease: 500 * time.Millisecond, RetryBase: time.Millisecond, MaxAttempts: 5}
	s, err := Open(filepath.Join(dir, "orders.db"), cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m, err := OpenMock(filepath.Join(dir, "provider.db"))
	if err != nil {
		t.Fatal(err)
	}
	m.Delay = 350 * time.Millisecond
	t.Cleanup(func() { m.Close() })
	server := httptest.NewServer(m.Handler())
	t.Cleanup(server.Close)
	c, err := NewClient(server.URL, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return s, m, &Worker{Store: s, Gateway: c}
}

func create(t *testing.T, s *Store, key, scenario string) Order {
	t.Helper()
	o, _, err := s.Create(context.Background(), key, 1200, scenario, "req_"+key)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func get(t *testing.T, s *Store, id string) Order {
	t.Helper()
	o, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func runUntil(t *testing.T, w *Worker, id, status string) Order {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := w.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
		o := get(t, w.Store, id)
		if o.Status == status {
			return o
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("expected %s, got %+v", status, get(t, w.Store, id))
	return Order{}
}

func TestConcurrentIdempotency(t *testing.T) {
	s, m, _ := setup(t)
	server := httptest.NewServer(Handler(s, m))
	defer server.Close()
	var wg sync.WaitGroup
	ids := make(chan string, 32)
	errs := make(chan error, 32)
	var accepted atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", server.URL+"/orders", strings.NewReader("{\"amount_minor\":1200}"))
			req.Header.Set("Idempotency-Key", "same_key")
			res, err := server.Client().Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer res.Body.Close()
			if res.StatusCode != 200 && res.StatusCode != 202 {
				errs <- fmt.Errorf("status %d", res.StatusCode)
				return
			}
			if res.StatusCode == 202 {
				accepted.Add(1)
			}
			var o Order
			if err = json.NewDecoder(res.Body).Decode(&o); err != nil {
				errs <- err
				return
			}
			ids <- o.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 || accepted.Load() != 1 || count(t, s, "orders") != 1 || count(t, s, "goqite") != 1 {
		t.Fatalf("duplicate work: ids=%d accepted=%d", len(seen), accepted.Load())
	}
}

func TestKeyConflict(t *testing.T) {
	s, m, _ := setup(t)
	create(t, s, "conflict", "normal")
	server := httptest.NewServer(Handler(s, m))
	defer server.Close()
	for _, body := range []string{"{\"amount_minor\":1201}", "{\"amount_minor\":1200,\"scenario\":\"reject\"}"} {
		req, _ := http.NewRequest("POST", server.URL+"/orders", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "conflict")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 409 {
			t.Fatalf("want conflict, got %d", res.StatusCode)
		}
	}
	if count(t, s, "orders") != 1 || count(t, s, "goqite") != 1 {
		t.Fatal("conflict created work")
	}
}

func TestAtomicOrderAndEnqueueRollback(t *testing.T) {
	s, _, _ := setup(t)
	_, err := s.db.Exec("CREATE TRIGGER fail_enqueue BEFORE INSERT ON goqite BEGIN SELECT RAISE(ABORT, 'injected'); END;")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Create(context.Background(), "atomic", 100, "normal", "req_atomic")
	if err == nil {
		t.Fatal("expected injected failure")
	}
	if count(t, s, "orders") != 0 || count(t, s, "goqite") != 0 {
		t.Fatal("half-committed order")
	}
}

func TestNormalSuccess(t *testing.T) {
	s, m, w := setup(t)
	o := create(t, s, "normal", "normal")
	done := runUntil(t, w, o.ID, "succeeded")
	e, err := m.Evidence(o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Attempts != 1 || done.Receipt == "" || e.Effects != 1 || e.Posts != 1 || count(t, s, "goqite") != 0 {
		t.Fatalf("bad success: %+v %+v", done, e)
	}
}

func TestCommittedTimeoutReconcilesWithoutResubmit(t *testing.T) {
	s, m, w := setup(t)
	o := create(t, s, "timeout", "commit_timeout")
	if _, err := w.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := get(t, s, o.ID)
	e, err := m.Evidence(o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "unknown" || first.LastError != "timeout" || e.Effects != 1 {
		t.Fatalf("lost uncertainty: %+v %+v", first, e)
	}
	done := runUntil(t, w, o.ID, "succeeded")
	e, _ = m.Evidence(o.ID)
	if done.Attempts != 2 || e.Posts != 1 || e.Effects != 1 {
		t.Fatalf("resubmitted accepted work: %+v %+v", done, e)
	}
}

func TestRetryAfterAuthoritativeNotFound(t *testing.T) {
	s, m, w := setup(t)
	o := create(t, s, "transient", "transient")
	done := runUntil(t, w, o.ID, "succeeded")
	e, _ := m.Evidence(o.ID)
	if done.Attempts != 3 || e.Posts != 3 || e.Effects != 1 {
		t.Fatalf("bad retry: %+v %+v", done, e)
	}
}

func TestRetryBudgetKeepsAmbiguousOutcome(t *testing.T) {
	for _, scenario := range []string{"unavailable", "query_unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, w := setup(t)
			s.cfg.MaxAttempts = 3
			o := create(t, s, "budget", scenario)
			done := runUntil(t, w, o.ID, "needs_review")
			e, _ := m.Evidence(o.ID)
			wantEffects := 0
			if scenario == "query_unavailable" {
				wantEffects = 1
			}
			if done.Attempts != 3 || e.Posts != 1 || e.Effects != wantEffects || count(t, s, "goqite") != 0 {
				t.Fatalf("bad exhausted state: %+v %+v", done, e)
			}
		})
	}
}

func TestPermanentRejection(t *testing.T) {
	s, m, w := setup(t)
	o := create(t, s, "reject", "reject")
	done := runUntil(t, w, o.ID, "rejected")
	e, _ := m.Evidence(o.ID)
	if done.Attempts != 1 || e.Effects != 0 || e.Posts != 1 {
		t.Fatalf("bad rejection %+v %+v", done, e)
	}
}

func TestMalformedResponseIsUnknown(t *testing.T) {
	s, m, w := setup(t)
	o := create(t, s, "malformed", "malformed")
	if _, err := w.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := get(t, s, o.ID)
	if first.Status != "unknown" || first.LastError != "invalid_provider_response" {
		t.Fatalf("trusted bad output: %+v", first)
	}
	runUntil(t, w, o.ID, "succeeded")
	e, _ := m.Evidence(o.ID)
	if e.Effects != 1 || e.Posts != 1 {
		t.Fatalf("bad reconciliation %+v", e)
	}
}

func TestRestartPending(t *testing.T) {
	s, m, w := setup(t)
	o := create(t, s, "restart", "normal")
	var path string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, s.cfg, s.log)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	w.Store = reopened
	runUntil(t, w, o.ID, "succeeded")
	e, _ := m.Evidence(o.ID)
	if e.Effects != 1 {
		t.Fatal("pending task not recovered")
	}
}

func expire(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec("UPDATE goqite SET timeout='2000-01-01T00:00:00.000Z'"); err != nil {
		t.Fatal(err)
	}
}

func TestStaleAndDuplicateMessagesCannotRegressTerminal(t *testing.T) {
	s, _, _ := setup(t)
	o := create(t, s, "stale", "normal")
	a, err := s.claim(context.Background())
	if err != nil || a == nil {
		t.Fatal(err)
	}
	expire(t, s)
	b, err := s.claim(context.Background())
	if err != nil || b == nil {
		t.Fatal(err)
	}
	if err = s.finish(context.Background(), b, Outcome{Status: "succeeded", Receipt: "sim_receipt"}); err != nil {
		t.Fatal(err)
	}
	for _, out := range []Outcome{{Code: "late_timeout"}, {Status: "rejected", Code: "late_reject"}} {
		if err = s.finish(context.Background(), a, out); !errors.Is(err, ErrStale) {
			t.Fatalf("stale result accepted: %v", err)
		}
	}
	if err = s.queue.Send(context.Background(), goqite.Message{Body: []byte(o.ID)}); err != nil {
		t.Fatal(err)
	}
	c, err := s.claim(context.Background())
	if err != nil || c != nil {
		t.Fatalf("terminal task reclaimed: %v", err)
	}
	done := get(t, s, o.ID)
	if done.Status != "succeeded" || done.Receipt != "sim_receipt" || count(t, s, "goqite") != 0 {
		t.Fatalf("terminal regressed %+v", done)
	}
}

func TestCrashOnLastAttemptReachesReview(t *testing.T) {
	s, _, _ := setup(t)
	s.cfg.MaxAttempts = 1
	o := create(t, s, "last_attempt", "normal")
	if c, err := s.claim(context.Background()); err != nil || c == nil {
		t.Fatal(err)
	}
	expire(t, s)
	if c, err := s.claim(context.Background()); err != nil || c != nil {
		t.Fatalf("budget not enforced %v", err)
	}
	if get(t, s, o.ID).Status != "needs_review" || count(t, s, "goqite") != 0 {
		t.Fatal("last-attempt crash stranded task")
	}
}

func TestCrashAfterProviderCommit(t *testing.T) {
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$", "-test.v")
	child.Env = append(os.Environ(), "ORDER_LAB_CRASH_DIR="+dir)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("helper did not crash at expected boundary: %v %s", err, output)
	}
	cfg := Config{Lease: 30 * time.Millisecond, RetryBase: time.Millisecond, MaxAttempts: 5}
	s, err := Open(filepath.Join(dir, "orders.db"), cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := OpenMock(filepath.Join(dir, "provider.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	server := httptest.NewServer(m.Handler())
	defer server.Close()
	c, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	o, err := scanOrder(s.db.QueryRow("SELECT " + columns + " FROM orders WHERE idem_key='crash'"))
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != "unknown" {
		t.Fatalf("pre-call uncertainty not durable: %+v", o)
	}
	done := runUntil(t, &Worker{Store: s, Gateway: c}, o.ID, "succeeded")
	e, _ := m.Evidence(o.ID)
	if done.Attempts != 2 || e.Posts != 1 || e.Effects != 1 {
		t.Fatalf("crash recovery duplicate %+v %+v", done, e)
	}
}

func TestCrashHelper(t *testing.T) {
	dir := os.Getenv("ORDER_LAB_CRASH_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	cfg := Config{Lease: 30 * time.Millisecond, RetryBase: time.Millisecond, MaxAttempts: 5}
	s, err := Open(filepath.Join(dir, "orders.db"), cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	m, err := OpenMock(filepath.Join(dir, "provider.db"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(m.Handler())
	c, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	create(t, s, "crash", "normal")
	claim, err := s.claim(context.Background())
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	out := c.Place(context.Background(), claim.Order)
	if out.Status != "succeeded" {
		t.Fatalf("not committed %+v", out)
	}
	// No finish, Close, graceful shutdown, or deferred handlers.
	os.Exit(23)
}

type observedGateway struct {
	base   Gateway
	active atomic.Int32
	max    atomic.Int32
}

func (g *observedGateway) Place(ctx context.Context, o Order) Outcome {
	n := g.active.Add(1)
	defer g.active.Add(-1)
	for {
		old := g.max.Load()
		if n <= old || g.max.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return Outcome{Code: "cancelled"}
	case <-time.After(5 * time.Millisecond):
	}
	return g.base.Place(ctx, o)
}
func (g *observedGateway) Lookup(ctx context.Context, o Order) Outcome { return g.base.Lookup(ctx, o) }

func TestBoundedWorkerPoolAndCancellation(t *testing.T) {
	s, m, w := setup(t)
	ids := make([]string, 16)
	for i := range ids {
		ids[i] = create(t, s, fmt.Sprintf("pool_%d", i), "normal").ID
	}
	observed := &observedGateway{base: w.Gateway}
	w.Gateway = observed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, 4) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var remaining int
		if err := s.db.QueryRow("SELECT count(*) FROM orders WHERE status!='succeeded'").Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pool did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pool leaked after cancellation")
	}
	if observed.max.Load() < 2 || observed.max.Load() > 4 {
		t.Fatalf("pool bound %d", observed.max.Load())
	}
	for _, id := range ids {
		e, _ := m.Evidence(id)
		if e.Effects != 1 {
			t.Fatalf("missing/duplicate effect %+v", e)
		}
	}
}

func TestHTTPValidationAndSafeLogs(t *testing.T) {
	s, m, _ := setup(t)
	var logs bytes.Buffer
	s.log = slog.New(slog.NewJSONHandler(&logs, nil))
	handler := Handler(s, m)
	tests := []struct{ key, body string }{
		{"", "{\"amount_minor\":100}"}, {"bad key", "{\"amount_minor\":100}"},
		{"ok", "{\"amount_minor\":0}"}, {"ok", "{\"amount_minor\":1.2}"},
		{"ok", "{\"amount_minor\":100,\"secret\":\"do-not-log-me\"}"},
		{"ok", "{\"amount_minor\":100} {}"}, {"ok", strings.Repeat("x", 5000)},
		{"ok", "{\"amount_minor\":100,\"scenario\":\"real_bank\"}"},
	}
	for _, tc := range tests {
		req := httptest.NewRequest("POST", "/orders", strings.NewReader(tc.body))
		req.Header.Set("Idempotency-Key", tc.key)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != 400 {
			t.Fatalf("want 400 got %d", rr.Code)
		}
	}
	req := httptest.NewRequest("POST", "/orders", strings.NewReader("{\"amount_minor\":100}"))
	req.Header.Set("Idempotency-Key", "log")
	req.Header.Set("X-Request-ID", "req_log")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 202 || rr.Header().Get("X-Request-ID") != "req_log" {
		t.Fatal("request id missing")
	}
	if !strings.Contains(logs.String(), "req_log") || strings.Contains(logs.String(), "do-not-log-me") {
		t.Fatal("unsafe or missing log")
	}
	if count(t, s, "orders") != 1 {
		t.Fatal("invalid input persisted")
	}
}

func TestGatewayRestrictionsAndOutputValidation(t *testing.T) {
	for _, base := range []string{"https://example.com", "http://example.com", "http://127.0.0.1@evil.com", "http://127.0.0.1:80/path", "http://127.0.0.1:80?x=1"} {
		if c, err := NewClient(base, time.Second); err == nil {
			c.Close()
			t.Fatalf("allowed external/ambiguous base %s", base)
		}
	}
	o := Order{ID: "business_id", Amount: 100, Scenario: "normal", RequestID: "req"}
	for _, body := range []string{
		"{\"id\":\"other\",\"amount_minor\":100,\"status\":\"succeeded\",\"receipt\":\"r\"}",
		"{\"id\":\"business_id\",\"amount_minor\":200,\"status\":\"succeeded\",\"receipt\":\"r\"}",
		"{\"id\":\"business_id\",\"amount_minor\":100,\"status\":\"succeeded\",\"receipt\":\"r\"} {}",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		c, err := NewClient(server.URL, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if out := c.Place(context.Background(), o); out.Status == "succeeded" || out.Code != "invalid_provider_response" {
			t.Fatalf("trusted mismatch %+v", out)
		}
		c.Close()
		server.Close()
	}
}

func TestProviderIdempotency(t *testing.T) {
	_, m, w := setup(t)
	o := Order{ID: "business", Amount: 100, Scenario: "normal", RequestID: "req"}
	for i := 0; i < 2; i++ {
		if out := w.Gateway.Place(context.Background(), o); out.Status != "succeeded" {
			t.Fatalf("failed %+v", out)
		}
	}
	o.Amount = 200
	if out := w.Gateway.Place(context.Background(), o); out.Status == "succeeded" {
		t.Fatal("provider accepted conflicting amount")
	}
	e, _ := m.Evidence("business")
	if e.Effects != 1 {
		t.Fatal("provider duplicated effect")
	}
}

func TestNotFound(t *testing.T) {
	s, _, _ := setup(t)
	if _, err := s.Get(context.Background(), "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want no rows got %v", err)
	}
}

func TestOversizedProviderResponseRejected(t *testing.T) {
	body := "{\"id\":\"business_id\",\"amount_minor\":100,\"status\":\"succeeded\",\"receipt\":\"r\"}" + strings.Repeat(" ", 4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	defer server.Close()
	c, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out := c.Place(context.Background(), Order{ID: "business_id", Amount: 100, RequestID: "req"})
	if out.Status == "succeeded" || out.Code != "invalid_provider_response" {
		t.Fatalf("oversized response accepted: %+v", out)
	}
}
