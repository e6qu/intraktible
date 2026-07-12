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
	notice, err := Notice(rec, settings.Settings, s.now())
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		httpx.JSON(w, http.StatusOK, map[string]string{"decision_id": decisionID, "notice": notice})
		return
	}
	httpx.Download(w, "text/markdown; charset=utf-8", "adverse-action-"+sanitize(decisionID)+".md", notice)
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
