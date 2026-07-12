// SPDX-License-Identifier: AGPL-3.0-or-later

package fairlending

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service exposes the fair-lending surface: the disparate-impact report, the
// per-flow config that parameterizes it, the workspace adverse-action settings, and
// adverse-action notice generation. Reads are folded from existing read models; the
// two configs are the only write side.
type Service struct {
	cmd   *Handler
	store store.Store
	now   func() time.Time
}

// New wires the fair-lending write side and read models to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st, now: func() time.Time { return time.Now().UTC() }}
}

// Routes registers the fair-lending endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/fairlending/report", s.report)
	mux.HandleFunc("GET /v1/fairlending/settings", s.getSettings)
	mux.HandleFunc("PUT /v1/fairlending/settings", s.setSettings)
	mux.HandleFunc("GET /v1/flows/{flow_id}/fairlending", s.getConfig)
	mux.HandleFunc("PUT /v1/flows/{flow_id}/fairlending", s.setConfig)
	mux.HandleFunc("GET /v1/decisions/{decision_id}/adverse-action", s.adverseAction)
	mux.HandleFunc("POST /v1/decisions/{decision_id}/adverse-action/issue", s.issueAdverseAction)
	// The adverse-action work queue: declines and whether each has had its notice
	// issued yet (the 30-day-clock surface a compliance operator works from).
	mux.HandleFunc("GET /v1/adverse-actions", s.adverseActions)
}

// report builds the disparate-impact report for a flow. flow is required; the
// attribute/favorable/threshold come from the query, or from the flow's stored
// config when the query omits them. Rendered as JSON (default), CSV, or Markdown.
func (s *Service) report(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	p := Params{FlowID: q.Get("flow"), Attribute: q.Get("attribute"), Environment: q.Get("env")}
	if p.FlowID == "" {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("flow is required"))
		return
	}
	if fav := q.Get("favorable"); fav != "" {
		d := policy.Disposition(fav)
		if d != policy.Approve && d != policy.Decline && d != policy.Refer {
			httpx.Error(w, http.StatusBadRequest, fmt.Errorf("favorable must be approve, decline, or refer"))
			return
		}
		p.Favorable = d
	}

	// When the request omits the attribute, fall back to the flow's stored config so
	// the report can run from its first-class settings rather than ad-hoc params.
	if p.Attribute == "" {
		cfg, found, err := ReadConfig(r.Context(), s.store, id, p.FlowID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			httpx.Error(w, http.StatusBadRequest, fmt.Errorf("attribute is required (no fair-lending config is set for this flow)"))
			return
		}
		p.Attribute, p.Favorable, p.Threshold = cfg.Attribute, cfg.Favorable, cfg.Threshold
	}

	rep, err := Build(r.Context(), s.store, id, p, s.now())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	switch q.Get("format") {
	case "csv":
		doc, err := CSV(rep)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		httpx.Download(w, "text/csv; charset=utf-8", "disparate-impact.csv", doc)
	case "md", "markdown":
		httpx.Download(w, "text/markdown; charset=utf-8", "disparate-impact.md", Markdown(rep))
	default:
		httpx.JSON(w, http.StatusOK, rep)
	}
}

func (s *Service) getConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	flowID := r.PathValue("flow_id")
	v, found, err := ReadConfig(r.Context(), s.store, id, flowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		v = ConfigView{FlowID: flowID}
	}
	httpx.JSON(w, http.StatusOK, v)
}

type configRequest struct {
	Attribute string             `json:"attribute"`
	Favorable policy.Disposition `json:"favorable"`
	Threshold float64            `json:"threshold"`
}

func (s *Service) setConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req configRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.SetConfig(r.Context(), id, r.PathValue("flow_id"), req.Attribute, req.Favorable, req.Threshold)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

func (s *Service) getSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	v, found, err := ReadSettings(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		v = SettingsView{}
	}
	httpx.JSON(w, http.StatusOK, v)
}

func (s *Service) setSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req Settings
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.SetSettings(r.Context(), id, req)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

// adverseAction renders the ECOA / Reg B notice for a declined decision. Markdown by
// default (the filed document); ?format=json returns { notice } for embedding. It is
// a 400, not a 500, when the decision is not a decline or the workspace has no
// creditor configured — those are caller-fixable states, not server faults.
func (s *Service) adverseAction(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	decisionID := r.PathValue("decision_id")
	rec, found, err := history.Read(r.Context(), s.store, id, decisionID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("decision %s not found", decisionID))
		return
	}
	settings, _, err := ReadSettings(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	opts := NoticeOptions{BasedOnConsumerReport: r.URL.Query().Get("consumer_report") == "true"}
	notice, err := Notice(rec, settings.Settings, opts, s.now())
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		issuance, issued, err := ReadIssuance(r.Context(), s.store, id, decisionID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		resp := map[string]any{"decision_id": decisionID, "notice": notice}
		if issued {
			resp["issuance"] = issuance
		}
		httpx.JSON(w, http.StatusOK, resp)
		return
	}
	httpx.Download(w, "text/markdown; charset=utf-8", "adverse-action-"+sanitize(decisionID)+".md", notice)
}

type issueRequest struct {
	Method                DeliveryMethod `json:"method"`
	BasedOnConsumerReport bool           `json:"based_on_consumer_report"`
}

// issueAdverseAction records that the adverse-action notice for a declined decision
// was served. It renders the notice exactly as the download does — so the recorded
// content hash is of the served document — and refuses (400) if the decision is not
// a decline, the creditor/CRA is unconfigured, or the notice carries no reasons.
func (s *Service) issueAdverseAction(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	decisionID := r.PathValue("decision_id")
	var req issueRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	rec, found, err := history.Read(r.Context(), s.store, id, decisionID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("decision %s not found", decisionID))
		return
	}
	settings, _, err := ReadSettings(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	opts := NoticeOptions{BasedOnConsumerReport: req.BasedOnConsumerReport}
	notice, err := Notice(rec, settings.Settings, opts, s.now())
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	hash, algo := HashNotice(notice)
	e, err := s.cmd.Issue(r.Context(), id, IssueCmd{
		DecisionID:            decisionID,
		Subject:               subjectOf(rec),
		Method:                req.Method,
		BasedOnConsumerReport: req.BasedOnConsumerReport,
		PrincipalReasons:      principalReasons(rec),
		ContentHash:           hash,
		HashAlgo:              algo,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

// adverseActionItem is one row of the work queue: a declined decision and whether its
// notice has been issued, with the age of the decision so the 30-day clock is visible.
type adverseActionItem struct {
	DecisionID string        `json:"decision_id"`
	FlowID     string        `json:"flow_id"`
	Slug       string        `json:"slug"`
	Subject    string        `json:"subject,omitempty"`
	DecidedAt  time.Time     `json:"decided_at"`
	AgeDays    int           `json:"age_days"`
	Issued     bool          `json:"issued"`
	Issuance   *IssuanceView `json:"issuance,omitempty"`
}

// adverseActions lists the tenant's declined decisions and their notice status.
// ?status=pending returns only declines with no issuance yet (the work to do);
// ?status=issued returns only those served; default returns both.
func (s *Service) adverseActions(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := history.List(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	issuances, err := ListIssuances(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	byDecision := make(map[string]IssuanceView, len(issuances))
	for _, iv := range issuances {
		byDecision[iv.DecisionID] = iv
	}
	status := r.URL.Query().Get("status")
	now := s.now()
	items := make([]adverseActionItem, 0)
	for _, rec := range recs {
		if policy.Disposition(rec.Disposition) != policy.Decline {
			continue
		}
		iv, issued := byDecision[rec.DecisionID]
		if (status == "pending" && issued) || (status == "issued" && !issued) {
			continue
		}
		decidedAt := rec.EndedAt
		if decidedAt.IsZero() {
			decidedAt = rec.StartedAt
		}
		item := adverseActionItem{
			DecisionID: rec.DecisionID, FlowID: rec.FlowID, Slug: rec.Slug, Subject: subjectOf(rec),
			DecidedAt: decidedAt, AgeDays: int(now.Sub(decidedAt).Hours() / 24), Issued: issued,
		}
		if issued {
			ivCopy := iv
			item.Issuance = &ivCopy
		}
		items = append(items, item)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"adverse_actions": items, "total": len(items)})
}

// subjectOf keys a decision's data subject as "type/id" — the same key consent, PII
// sealing, and erasure use — or "" when the decision referenced no entity.
func subjectOf(rec history.Record) string {
	if rec.EntityType == "" || rec.EntityID == "" {
		return ""
	}
	return rec.EntityType + "/" + rec.EntityID
}

// sanitize keeps a decision id safe for a download filename.
func sanitize(s string) string {
	if s == "" {
		return "notice"
	}
	var b []rune
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b = append(b, r)
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}
