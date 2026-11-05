package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"time"
)

type Outcome struct{ Status, Receipt, Code string }

type Gateway interface {
	Place(context.Context, Order) Outcome
	Lookup(context.Context, Order) Outcome
}

type Worker struct {
	Store   *Store
	Gateway Gateway
}

func (w *Worker) Once(ctx context.Context) (bool, error) {
	c, err := w.Store.claim(ctx)
	if err != nil || c == nil {
		return false, err
	}
	var out Outcome
	if c.first {
		out = w.Gateway.Place(ctx, c.Order)
	} else {
		// Unknown is NOT failed. Ask the same provider using the same business ID.
		out = w.Gateway.Lookup(ctx, c.Order)
		if out.Status == "not_found" {
			out = w.Gateway.Place(ctx, c.Order)
		}
	}
	err = w.Store.finish(ctx, c, out)
	if errors.Is(err, ErrStale) {
		w.Store.log.Warn("stale_result_ignored", "order_id", c.ID, "request_id", c.RequestID, "version", c.Version)
		return true, nil
	}
	return true, err
}

// Run owns a fixed goroutine pool. No DB transaction is held over HTTP.
func (w *Worker) Run(ctx context.Context, n int) error {
	if n < 1 || n > 32 {
		return errors.New("workers must be between 1 and 32")
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				worked, err := w.Once(ctx)
				if err != nil && ctx.Err() == nil {
					w.Store.log.Error("worker_error", "error", err.Error())
				}
				if !worked || err != nil {
					timer := time.NewTimer(20 * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}
		}()
	}
	wg.Wait()
	return nil
}

type Client struct {
	base string
	http *http.Client
}

func NewClient(base string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || timeout <= 0 {
		return nil, errors.New("gateway must be a plain loopback HTTP URL with a positive timeout")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() {
		return nil, errors.New("only numeric loopback gateway addresses are allowed")
	}
	return &Client{base: base, http: &http.Client{
		Timeout: timeout,
		// No environment proxy and no redirect to an external endpoint.
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

type providerRecord struct {
	ID       string `json:"id"`
	Amount   int64  `json:"amount_minor"`
	Scenario string `json:"scenario,omitempty"`
	Status   string `json:"status"`
	Receipt  string `json:"receipt"`
}

func (c *Client) Place(ctx context.Context, o Order) Outcome {
	body, _ := json.Marshal(providerRecord{ID: o.ID, Amount: o.Amount, Scenario: o.Scenario})
	return c.call(ctx, http.MethodPost, "/orders", body, o)
}

func (c *Client) Lookup(ctx context.Context, o Order) Outcome {
	return c.call(ctx, http.MethodGet, "/orders/"+url.PathEscape(o.ID), nil, o)
}

func (c *Client) call(ctx context.Context, method, path string, body []byte, o Order) Outcome {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return Outcome{Code: "request_error"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", o.RequestID)
	req.Header.Set("Idempotency-Key", o.ID)
	res, err := c.http.Do(req)
	if err != nil {
		code := "network_error"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "timeout"
		}
		return Outcome{Code: code}
	}
	defer res.Body.Close()
	// 404 is authoritative ONLY because this lab provider promises a consistent lookup.
	if method == http.MethodGet && res.StatusCode == http.StatusNotFound {
		return Outcome{Status: "not_found"}
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusUnprocessableEntity {
		return Outcome{Code: "unexpected_http_status"}
	}
	var r providerRecord
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, 4097))
	if readErr != nil || len(raw) > 4096 {
		return Outcome{Code: "invalid_provider_response"}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&r); err != nil {
		return Outcome{Code: "invalid_provider_response"}
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF || r.ID != o.ID || r.Amount != o.Amount {
		return Outcome{Code: "invalid_provider_response"}
	}
	if res.StatusCode == http.StatusOK && r.Status == "succeeded" && tokenPattern.MatchString(r.Receipt) {
		return Outcome{Status: "succeeded", Receipt: r.Receipt}
	}
	if res.StatusCode == http.StatusUnprocessableEntity && r.Status == "rejected" {
		return Outcome{Status: "rejected", Code: "provider_rejected"}
	}
	return Outcome{Code: "invalid_provider_response"}
}
