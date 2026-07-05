// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sort"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/server"
)

// The demo cast (ported from the retired TS seed roster) plus the service
// identities the workspace's machine traffic runs under. Actors are plain
// handles (not emails) so @-mentions in comments resolve to real inboxes —
// the notifications projector keys recipients on the mentioned handle.
const (
	actorDev       = "dev"
	actorAva       = "ava.chen"        // admin — Head of Decisioning
	actorMarcus    = "marcus.reed"     // approver — Risk Approver
	actorPriya     = "priya.nair"      // editor — Flow Author
	actorDiego     = "diego.santos"    // operator — Case Analyst
	actorLena      = "lena.hoff"       // viewer — Audit & Compliance
	actorSvcProd   = "svc-prod"        // production decide traffic
	actorSvcCI     = "svc-ci"          // sandbox decide traffic
	actorSvcSched  = "svc-scheduler"   // SLA sweeps / scheduled checks
	actorSvcBI     = "svc-bi"          // analytics read-only (rotated)
	actorSvcOldPtr = "svc-partner"     // decommissioned partner (revoked)
	devAPIKey      = "dev-sandbox-key" // #nosec G101 -- the well-known public dev key, not a secret
)

// seeder drives the assembled backend's http.Handler in-process: the same REST
// calls a user would make, authenticated per actor, with fail-fast semantics.
type seeder struct {
	srv  *server.Server
	log  *eventlog.MemoryLog
	clk  *scriptedClock
	prov *scriptedProvider

	keys map[string]string // actor -> API key secret
	ids  map[string]string // seed tag (e.g. "flow:credit-decision") -> real id
}

// call performs one request as actor and decodes the JSON response into out
// (out may be nil). Any non-2xx response aborts the seed with the body printed.
func (s *seeder) call(actor, method, path string, body, out any) {
	var reqBody []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			fatalf("marshal %s %s: %v", method, path, err)
		}
		reqBody = b
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	key, ok := s.keys[actor]
	if !ok {
		fatalf("%s %s: no API key for actor %q", method, path, actor)
	}
	req.Header.Set("X-Api-Key", key)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)
	if w.Code < 200 || w.Code > 299 {
		fatalf("%s %s as %s -> %d\n%s", method, path, actor, w.Code, w.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			fatalf("%s %s: decode response: %v\n%s", method, path, err, w.Body.String())
		}
	}
	s.waitProjected()
}

// waitProjected blocks until the projection runtime has applied every event the
// log holds, so a read issued right after a write sees it (the runtime consumes
// the in-process bus asynchronously).
func (s *seeder) waitProjected() {
	head := s.log.Head()
	for i := 0; s.srv.Projections.Applied() < head; i++ {
		if i > 20000 {
			fatalf("projections stalled at %d (log head %d): %v", s.srv.Projections.Applied(), head, s.srv.Projections.Err())
		}
		time.Sleep(100 * time.Microsecond)
	}
	if err := s.srv.Projections.Err(); err != nil {
		fatalf("projection error: %v", err)
	}
}

// id returns the real id registered under tag, failing fast on a typo'd tag.
func (s *seeder) id(tag string) string {
	v, ok := s.ids[tag]
	if !ok {
		fatalf("seed: unknown id tag %q", tag)
	}
	return v
}

func (s *seeder) setID(tag, id string) {
	if id == "" {
		fatalf("seed: empty id for tag %q", tag)
	}
	s.ids[tag] = id
}

// action is one scheduled step of the seed timeline.
type action struct {
	at   time.Time
	name string
	run  func()
}

// runTimeline executes actions in chronological order (stable for equal times),
// moving the scripted clock to each action's time first.
func (s *seeder) runTimeline(actions []action) {
	sort.SliceStable(actions, func(i, j int) bool { return actions[i].at.Before(actions[j].at) })
	for _, a := range actions {
		s.clk.Set(a.at)
		a.run()
	}
}

func fatalf(format string, args ...any) {
	panic(fmt.Sprintf(format, args...))
}
