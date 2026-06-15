// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/e6qu/intraktible/decision-engine/export"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/httpx"
)

// exportFlow renders a flow version as a diagram: ?format=mermaid (flowchart, the
// default) | mermaid-state | bpmn, and ?version=N (defaults to the latest).
func (s *Service) exportFlow(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	fv, found, err := flows.Read(r.Context(), s.store, id, r.PathValue("flow_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("flow not found"))
		return
	}
	ver, err := pickVersion(fv, r.URL.Query().Get("version"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "mermaid"
	}
	var body, contentType, filename string
	switch format {
	case "mermaid", "flowchart":
		body, contentType, filename = export.MermaidFlowchart(ver.Graph), "text/plain; charset=utf-8", fv.Slug+".mmd"
	case "mermaid-state", "state":
		body, contentType, filename = export.MermaidState(ver.Graph), "text/plain; charset=utf-8", fv.Slug+"-state.mmd"
	case "bpmn":
		body, contentType, filename = export.BPMN(ver.Graph, fv.Name), "application/xml; charset=utf-8", fv.Slug+".bpmn"
	default:
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("unknown export format %q (mermaid|mermaid-state|bpmn)", format))
		return
	}
	writeExport(w, contentType, filename, body)
}

// exportDecision renders one recorded decision run as a Mermaid sequence diagram.
func (s *Service) exportDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	rec, found, err := history.Read(r.Context(), s.store, id, r.PathValue("decision_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("decision not found"))
		return
	}
	steps := make([]export.RunStep, 0, len(rec.Nodes))
	for _, n := range rec.Nodes {
		steps = append(steps, export.RunStep{NodeID: n.NodeID, Type: string(n.Type)})
	}
	body := export.MermaidSequence(rec.Slug, steps, rec.Status)
	writeExport(w, "text/plain; charset=utf-8", rec.DecisionID+"-trace.mmd", body)
}

// pickVersion returns the requested version (or the latest when v is empty).
func pickVersion(fv flows.FlowView, v string) (flows.VersionView, error) {
	want := fv.Latest
	if v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return flows.VersionView{}, fmt.Errorf("version must be a number, got %q", v)
		}
		want = n
	}
	for _, ver := range fv.Versions {
		if ver.Version == want {
			return ver, nil
		}
	}
	return flows.VersionView{}, fmt.Errorf("flow has no version %d", want)
}

func writeExport(w http.ResponseWriter, contentType, filename, body string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}
