// SPDX-License-Identifier: AGPL-3.0-or-later

// intraktible-seed builds the demo workspace as a REAL event log: it assembles
// the actual backend in-process (server.New over an in-memory log/store, a
// scripted clock, and a scripted AI provider), drives its own HTTP handler
// through the same REST calls a user would make — key minting, flow authoring,
// maker-checker deployments, a month of decide traffic, case work, agent runs,
// governance — then exports the event history as the JSON asset the wasm
// deployment replays at boot.
//
// Regeneration is explicit (`make demo-seed`); event ids come from crypto/rand,
// so two runs differ in ids while carrying the same story.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/server"
)

func main() {
	out := flag.String("out", "web/static/demo-seed.json", "path for the exported event-log JSON")
	flag.Parse()

	events := buildSeed()
	b, err := json.Marshal(events)
	if err != nil {
		fatalf("marshal seed: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil { // #nosec G301 -- public build-artifact directory
		fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(*out, b, 0o600); err != nil {
		fatalf("write seed: %v", err)
	}
	fmt.Printf("seed: %d events, %.2f MB -> %s\n", len(events), float64(len(b))/(1<<20), *out)

	if err := writeRoster(filepath.Join(filepath.Dir(*out), "demo-users.json")); err != nil {
		fatalf("write roster: %v", err)
	}
	verifySeed(events)
}

// writeRoster emits the demo cast next to the seed. Managed API keys are
// operational state (secret hashes never enter the event log), so a replayed
// deployment cannot resolve the seeded keys — the demo shell mints fresh
// session keys for these identities at boot, via the dev admin key.
func writeRoster(path string) error {
	type demoUser struct {
		Actor string `json:"actor"`
		Name  string `json:"name"`
		Role  string `json:"role"`
		Title string `json:"title"`
		Scope string `json:"scope"`
	}
	roster := []demoUser{
		{actorAva, "Ava Chen", "admin", "Head of Decisioning", "*"},
		{actorMarcus, "Marcus Reed", "approver", "Risk Approver", "*"},
		{actorPriya, "Priya Nair", "editor", "Flow Author", "*"},
		{actorDiego, "Diego Santos", "operator", "Case Analyst", "*"},
		{actorLena, "Lena Hoff", "viewer", "Audit & Compliance", "*"},
	}
	b, err := json.MarshalIndent(roster, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// buildSeed runs the full scripted history and returns the exported event log.
func buildSeed() []eventlog.Envelope {
	anchor := time.Now().UTC().Truncate(time.Hour)

	// Lay the decision plan out first: the traffic window plus the entity-event
	// backfill define where the configuration window must start.
	slots := buildTrafficPlan(anchor)
	assignPreApprovalRefs(slots, anchor)
	earliest := slots[0].at
	for _, e := range entitySpecs() {
		for _, ev := range e.events {
			if t := anchor.Add(-time.Duration(ev.hrs * float64(time.Hour))); t.Before(earliest) {
				earliest = t
			}
		}
	}
	cfgStart := earliest.Add(-46 * time.Hour)

	clk := newScriptedClock(cfgStart)
	prov := newScriptedProvider(clk)

	log := eventlog.NewMemory()
	st := store.NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := server.New(ctx, server.Config{
		Modules:    "all",
		DevAPIKey:  devAPIKey,
		StoreKind:  "memory",
		Now:        clk.Now,
		AIProvider: prov,
	}, log, st)
	if err != nil {
		fatalf("assemble server: %v", err)
	}

	s := &seeder{
		srv: srv, log: log, clk: clk, prov: prov,
		keys: map[string]string{actorDev: devAPIKey},
		ids:  map[string]string{},
	}
	s.registerAINodeOutputs()

	// Configuration window: keys -> context layer -> agents -> flows -> policies
	// -> deployments (maker-checker) -> governance. The cursor keeps every step
	// strictly ordered inside the window.
	cfg := &timeCursor{t: cfgStart}
	var acts []action
	acts = append(acts, s.keyActions(cfg)...)
	acts = append(acts, s.contextConfigActions(cfg)...)
	acts = append(acts, s.agentConfigActions(cfg)...)
	acts = append(acts, s.flowActions(cfg)...)
	acts = append(acts, s.policyActions(cfg)...)
	acts = append(acts, s.deployActions(cfg)...)
	acts = append(acts, s.governanceConfigActions(cfg)...)
	acts = append(acts, s.preApprovalActions(cfg, anchor)...)
	if cfg.t.After(earliest) {
		fatalf("configuration window overruns the traffic start by %s", cfg.t.Sub(earliest))
	}

	// The 30-day timeline: entity backfill, decide traffic, case work, hygiene,
	// SLA sweeps, agent runs, grants, schedules, key lifecycle, baselines,
	// discussions, pending requests, monitor checks, inbox reads.
	bySeed := s.designateCaseSources(slots, anchor)
	acts = append(acts, s.entityEventActions(anchor)...)
	acts = append(acts, s.decisionActions(slots)...)
	acts = append(acts, s.caseWorkActions(bySeed, anchor)...)
	// Membership is re-derived per hygiene pass: designated slots gain their
	// decision ids as the traffic runs.
	acts = append(acts, s.hygieneActions(slots[0].at, anchor, func(decisionID string) bool {
		if decisionID == "" {
			return false
		}
		for _, slot := range slots {
			if slot.designated && slot.decisionID == decisionID {
				return true
			}
		}
		return false
	})...)
	acts = append(acts, s.slaSweepActions(slots[0].at, anchor)...)
	acts = append(acts, s.agentRunActions(anchor)...)
	acts = append(acts, s.grantActions(anchor)...)
	acts = append(acts, s.scheduleActions(anchor)...)
	acts = append(acts, s.keyLifecycleActions(anchor)...)
	acts = append(acts, s.baselineActions(slots, anchor)...)
	acts = append(acts, s.commentActions(bySeed, anchor)...)
	acts = append(acts, s.pendingRequestActions(anchor)...)
	acts = append(acts, s.monitorCheckActions(anchor)...)
	acts = append(acts, s.inboxActions(anchor)...)
	acts = append(acts, s.adverseActionActions(anchor)...)
	acts = append(acts, s.reconsiderationActions(anchor)...)
	acts = append(acts, s.dataGovernanceActions(anchor)...)

	s.runTimeline(acts)

	// The last beat: an async agent run left mid-flight, exported as "running".
	s.clk.Set(anchor.Add(-2 * time.Minute))
	s.startRunningRun()

	events := log.Export()

	// Unpark the blocked worker and shut down cleanly (its terminal event lands
	// after the export, so the seed keeps the run running).
	s.prov.release(runningPrompt)
	cancel()
	srv.Close()
	if err := log.Close(); err != nil {
		fatalf("close log: %v", err)
	}
	return events
}

// verifySeed replays the exported history through a fresh server — the exact
// wasm boot path — and spot-checks the projections, failing loudly on drift.
func verifySeed(events []eventlog.Envelope) {
	log, err := eventlog.NewMemoryFrom(events)
	if err != nil {
		fatalf("round trip: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := server.New(ctx, server.Config{Modules: "all", DevAPIKey: devAPIKey, StoreKind: "memory"},
		log, store.NewMemory())
	if err != nil {
		cancel()
		fatalf("round trip: assemble: %v", err)
	}
	// Cancel before Close: the async-run workers stop on ctx cancellation, and
	// Close waits for them (the recovered "running" run drains here).
	defer func() {
		cancel()
		srv.Close()
	}()
	assertRunningRun(events)
	counts := spotCheck(srv)
	fmt.Printf("round trip OK: %s\n", counts)
}

// assertRunningRun checks the exported history carries exactly one agent run
// left mid-flight: one AgentRunStarted whose run never recorded a terminal.
func assertRunningRun(events []eventlog.Envelope) {
	started := map[string]bool{}
	for _, e := range events {
		var p struct {
			RunID string `json:"run_id"`
		}
		switch e.Type {
		case "agents.run_started":
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				fatalf("round trip: decode run_started: %v", err)
			}
			started[p.RunID] = true
		case "agents.run_recorded":
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				fatalf("round trip: decode run_recorded: %v", err)
			}
			delete(started, p.RunID)
		}
	}
	if len(started) != 1 {
		fatalf("round trip: %d runs left mid-flight in the seed, want 1", len(started))
	}
}

// spotCheck reads the replayed projections through the API and asserts the
// headline counts; it returns a printable summary.
func spotCheck(srv *server.Server) string {
	get := func(path string, out any) {
		req := httptest.NewRequest("GET", path, http.NoBody)
		req.Header.Set("X-Api-Key", devAPIKey)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)
		if w.Code != 200 {
			fatalf("round trip: GET %s -> %d\n%s", path, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			fatalf("round trip: GET %s: %v", path, err)
		}
	}
	// The projections rebuild synchronously inside server.New (bootstrap), so
	// reads are immediately consistent here.
	var flows struct {
		Flows []struct {
			Slug        string `json:"slug"`
			Description string `json:"description"`
		} `json:"flows"`
	}
	get("/v1/flows", &flows)
	requireEq("flows", len(flows.Flows), 10)
	for _, f := range flows.Flows {
		if f.Description == "" {
			fatalf("round trip: flow %s has no description", f.Slug)
		}
	}

	// The fixed-key discussion threads (agents/model/entity are addressed by
	// natural keys, so they are checkable without seed-run ids).
	comments := func(subjectType, subjectID string) int {
		var res struct {
			Comments []json.RawMessage `json:"comments"`
		}
		get("/v1/comments/"+subjectType+"/"+url.PathEscape(subjectID), &res)
		return len(res.Comments)
	}
	requireEq("aml-narrative agent comments", comments("agent", "aml-narrative"), 2)
	requireEq("fraud-explainer agent comments", comments("agent", "fraud-explainer"), 2)
	requireEq("claim_fraud model comments", comments("model", "claim_fraud"), 2)
	requireEq("applicant/APP-1002 entity comments", comments("entity", "applicant/APP-1002"), 2)

	var decisions struct {
		Total int `json:"total"`
	}
	get("/v1/decisions?limit=1", &decisions)
	requireEq("decisions", decisions.Total, len(trafficPattern)*trafficRotations)

	var suspended struct {
		Total int `json:"total"`
	}
	get("/v1/decisions?limit=1&status=suspended", &suspended)
	requireEq("suspended decisions", suspended.Total, 3)

	var failed struct {
		Total int `json:"total"`
	}
	get("/v1/decisions?limit=1&status=failed", &failed)
	requireEq("failed decisions", failed.Total, 14)

	// Adverse-action notices: the credit flow's declines split into issued and still-
	// pending, so both sides of the Fair-lending work queue are populated.
	var issued, pending struct {
		AdverseActions []struct {
			DecisionID string `json:"decision_id"`
		} `json:"adverse_actions"`
	}
	get("/v1/adverse-actions?status=issued", &issued)
	get("/v1/adverse-actions?status=pending", &pending)
	if len(issued.AdverseActions) == 0 {
		fatalf("round trip: no adverse-action notices were issued")
	}
	// The Art. 22 explanation renders for a declined decision.
	explReq := httptest.NewRequest("GET", "/v1/decisions/"+url.PathEscape(issued.AdverseActions[0].DecisionID)+"/explanation", http.NoBody)
	explReq.Header.Set("X-Api-Key", devAPIKey)
	explRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(explRec, explReq)
	if explRec.Code != 200 || !strings.Contains(explRec.Body.String(), "How this decision was made") {
		fatalf("round trip: decision explanation -> %d\n%s", explRec.Code, explRec.Body.String())
	}
	if len(pending.AdverseActions) == 0 {
		fatalf("round trip: no adverse-action notices left pending (queue would look empty)")
	}

	// Human reviews (Art. 22 / reconsideration): a couple of automated declines were
	// reviewed by a person, one overturned and one upheld.
	var reconsiderations struct {
		Reconsiderations []struct {
			Outcome string `json:"outcome"`
		} `json:"reconsiderations"`
	}
	get("/v1/reconsiderations", &reconsiderations)
	if len(reconsiderations.Reconsiderations) < 2 {
		fatalf("round trip: %d human reviews, want >= 2", len(reconsiderations.Reconsiderations))
	}

	// Contests: at least one still awaiting review (an open item on the compliance surface).
	var openContests struct {
		Contests []json.RawMessage `json:"contests"`
	}
	get("/v1/contests?status=open", &openContests)
	if len(openContests.Contests) == 0 {
		fatalf("round trip: no contests awaiting review (queue would look empty)")
	}

	// The adverse-action register exports as CSV with its header and at least one issued row.
	regReq := httptest.NewRequest("GET", "/v1/compliance/registers/adverse-actions", http.NoBody)
	regReq.Header.Set("X-Api-Key", devAPIKey)
	regRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(regRec, regReq)
	if regRec.Code != 200 || !strings.Contains(regRec.Body.String(), "decision_id,subject") {
		fatalf("round trip: adverse-action register -> %d\n%s", regRec.Code, regRec.Body.String())
	}

	// GLBA sharing opt-outs: a couple of subjects declined NPI sharing.
	var sharingRecs struct {
		Records []struct {
			OptedOut bool `json:"opted_out"`
		} `json:"records"`
	}
	get("/v1/sharing/records", &sharingRecs)
	optedOut := 0
	for _, rec := range sharingRecs.Records {
		if rec.OptedOut {
			optedOut++
		}
	}
	if optedOut < 2 {
		fatalf("round trip: %d sharing opt-outs, want >= 2", optedOut)
	}

	var cases struct {
		Cases []struct {
			Status string `json:"status"`
		} `json:"cases"`
	}
	get("/v1/cases", &cases)
	if len(cases.Cases) < 30 {
		fatalf("round trip: %d cases, want >= 30", len(cases.Cases))
	}

	var agents struct {
		Agents []json.RawMessage `json:"agents"`
	}
	get("/v1/agents", &agents)
	requireEq("agents", len(agents.Agents), 7)

	var runs struct {
		Runs []struct {
			Status string `json:"status"`
		} `json:"runs"`
	}
	get("/v1/agent-runs", &runs)
	// The run exported mid-flight is re-enqueued by crash recovery on boot, so
	// its live status here depends on worker timing; the raw seed carries it as
	// "running" (asserted in verifySeed over the envelopes).
	requireEq("agent runs", len(runs.Runs), len(runSeeds())+1)

	var models struct {
		Models []json.RawMessage `json:"models"`
	}
	get("/v1/models", &models)
	requireEq("models", len(models.Models), 7)

	var pas struct {
		PreApprovals []json.RawMessage `json:"preapprovals"`
	}
	get("/v1/preapprovals", &pas)
	requireEq("preapprovals", len(pas.PreApprovals), 8)

	var hooks struct {
		Webhooks []json.RawMessage `json:"webhooks"`
	}
	get("/v1/webhooks", &hooks)
	requireEq("webhooks", len(hooks.Webhooks), 3)

	monitors := 0
	for _, f := range []string{"credit-decision", "aml-screening", "card-fraud", "kyc-onboarding",
		"dispute-triage", "merchant-onboarding", "collections-hardship", "claim-triage", "payout-risk"} {
		monitors += monitorsOf(srv, f)
	}
	requireEq("monitors", monitors, len(monitorSpecs()))

	var inbox struct {
		Notifications []json.RawMessage `json:"notifications"`
	}
	get("/v1/notifications", &inbox)
	if len(inbox.Notifications) < 12 {
		fatalf("round trip: %d notifications, want >= 12 (dev sees the shared reviewer queue)", len(inbox.Notifications))
	}

	return fmtName("10 flows, %d decisions (%d failed, %d suspended), %d cases, 7 agents, %d runs, 7 models, 8 preapprovals, %d monitors, %d notifications",
		decisions.Total, failed.Total, suspended.Total, len(cases.Cases), len(runs.Runs), monitors, len(inbox.Notifications))
}

// monitorsOf counts a flow's monitors through the API.
func monitorsOf(srv *server.Server, slug string) int {
	req := httptest.NewRequest("GET", "/v1/flows", http.NoBody)
	req.Header.Set("X-Api-Key", devAPIKey)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)
	var flows struct {
		Flows []struct {
			FlowID string `json:"flow_id"`
			Slug   string `json:"slug"`
		} `json:"flows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &flows); err != nil {
		fatalf("round trip: list flows: %v", err)
	}
	for _, f := range flows.Flows {
		if f.Slug != slug {
			continue
		}
		req := httptest.NewRequest("GET", "/v1/flows/"+f.FlowID+"/monitors", http.NoBody)
		req.Header.Set("X-Api-Key", devAPIKey)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)
		var res struct {
			Monitors []json.RawMessage `json:"monitors"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			fatalf("round trip: monitors of %s: %v", slug, err)
		}
		return len(res.Monitors)
	}
	fatalf("round trip: flow %s not found", slug)
	return 0
}

func requireEq(what string, got, want int) {
	if got != want {
		fatalf("round trip: %s = %d, want %d", what, got, want)
	}
}
