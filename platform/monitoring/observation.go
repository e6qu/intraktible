// SPDX-License-Identifier: AGPL-3.0-or-later

// Package monitoring publishes the deployment-neutral application observation
// consumed by Shauth. It owns only process and projection evidence; tenant data
// never enters this boundary.
package monitoring

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const SchemaVersion = "e6qu.monitoring/v2"

type TokenDigest [sha256.Size]byte

// TokenDigestFromEnvironment validates the optional deployment token and
// retains only its digest. An absent token leaves the endpoint fail-closed.
func TokenDigestFromEnvironment() (*TokenDigest, error) {
	token, present := os.LookupEnv("INTRAKTIBLE_MONITORING_TOKEN")
	if !present || token == "" {
		return nil, nil
	}
	if len(token) < 32 || strings.IndexFunc(token, func(character rune) bool {
		return character <= ' ' || character == '\u007f'
	}) >= 0 {
		return nil, errors.New("INTRAKTIBLE_MONITORING_TOKEN must contain at least 32 non-whitespace characters")
	}
	digest := TokenDigest(sha256.Sum256([]byte(token)))
	return &digest, nil
}

type State struct {
	Health            string
	ProjectionApplied uint64
	EventHead         uint64
}

type observation struct {
	SchemaVersion string     `json:"schema_version"`
	ObservedAt    time.Time  `json:"observed_at"`
	Resources     []resource `json:"resources"`
}

type resource struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Health  string   `json:"health"`
	Metrics []metric `json:"metrics"`
}

type metric struct {
	Name   string  `json:"name"`
	Label  string  `json:"label"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Status string  `json:"status"`
}

func available(name, label string, value float64, unit string) metric {
	return metric{Name: name, Label: label, Value: value, Unit: unit, Status: "available"}
}

// Handler returns an authenticated observation endpoint. snapshot must return
// only fixed-cardinality deployment evidence.
func Handler(token *TokenDigest, snapshot func() State) http.Handler {
	if snapshot == nil {
		panic("monitoring snapshot function is required")
	}
	startedAt := time.Now()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r.Header.Get("Authorization"), token) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("WWW-Authenticate", `Bearer realm="intraktible-monitoring"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		state := snapshot()
		lag := uint64(0)
		if state.EventHead > state.ProjectionApplied {
			lag = state.EventHead - state.ProjectionApplied
			state.Health = "unhealthy"
		}
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		document := observation{
			SchemaVersion: SchemaVersion,
			ObservedAt:    time.Now().UTC(),
			Resources: []resource{{
				ID: "intraktible-process", Name: "Intraktible", Kind: "application", Health: state.Health,
				Metrics: []metric{
					available("projection.applied", "Applied event sequence", float64(state.ProjectionApplied), "events"),
					available("events.head", "Event log head", float64(state.EventHead), "events"),
					available("projection.lag", "Projection lag", float64(lag), "events"),
					available("process.goroutines", "Process goroutines", float64(runtime.NumGoroutine()), "goroutines"),
					available("process.heap", "Allocated heap", float64(memory.HeapAlloc)/(1024*1024), "MiB"),
					available("process.uptime", "Process uptime", time.Since(startedAt).Seconds(), "seconds"),
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(document); err != nil {
			slog.Error("write monitoring observation", "error", err)
		}
	})
}

func authorized(header string, expected *TokenDigest) bool {
	if expected == nil || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	actual := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	return subtle.ConstantTimeCompare(expected[:], actual[:]) == 1
}
