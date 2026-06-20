// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
)

// decideStatus drives the batch/stream error handling: a client-cause error rejects
// just the row, while a default (infrastructure) cause is a 500 that aborts the
// batch — so misclassifying an infra failure as a permanent per-row rejection cannot
// happen. Pin the cause→status mapping.
func TestDecideStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"bad request", command.ErrBadRequest, http.StatusBadRequest},
		{"wrapped bad request", fmt.Errorf("row: %w", command.ErrBadRequest), http.StatusBadRequest},
		{"not found", command.ErrNotFound, http.StatusNotFound},
		{"infra/unknown", errors.New("store: connection refused"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got := decideStatus(c.err); got != c.want {
			t.Errorf("%s: decideStatus = %d, want %d", c.name, got, c.want)
		}
	}
}
