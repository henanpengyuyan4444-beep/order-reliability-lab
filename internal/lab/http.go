package lab

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Handler(s *Store, m *MockProvider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]string{"status": "ok", "mode": "local-learning-only"})
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if !tokenPattern.MatchString(requestID) {
			requestID = newID()
		}
		w.Header().Set("X-Request-ID", requestID)
		var in struct {
			Amount   int64  `json:"amount_minor"`
			Scenario string `json:"scenario"`
		}
		if err := decode(w, r, &in); err != nil {
			respond(w, 400, map[string]string{"error": "invalid_json"})
			return
		}
		o, created, err := s.Create(r.Context(), r.Header.Get("Idempotency-Key"), in.Amount, in.Scenario, requestID)
		if errors.Is(err, ErrInvalid) {
			respond(w, 400, map[string]string{"error": "invalid_input"})
			return
		}
		if errors.Is(err, ErrConflict) {
			respond(w, 409, map[string]string{"error": "idempotency_conflict"})
			return
		}
		if err != nil {
			s.log.Error("create_error", "request_id", requestID, "error", err.Error())
			respond(w, 500, map[string]string{"error": "storage_error"})
			return
		}
		status := 200
		if created {
			status = 202
		}
		respond(w, status, o)
	})
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		o, err := s.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, sql.ErrNoRows) {
			respond(w, 404, map[string]string{"error": "not_found"})
			return
		}
		if err != nil {
			respond(w, 500, map[string]string{"error": "storage_error"})
			return
		}
		w.Header().Set("X-Request-ID", o.RequestID)
		respond(w, 200, o)
	})
	mux.HandleFunc("GET /lab/effects/{id}", func(w http.ResponseWriter, r *http.Request) {
		e, err := m.Evidence(r.PathValue("id"))
		if err != nil {
			respond(w, 500, map[string]string{"error": "storage_error"})
			return
		}
		respond(w, 200, e)
	})
	return mux
}
