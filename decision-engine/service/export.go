// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/e6qu/intraktible/decision-engine/export"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/httpx"
)

// exportFlow renders a flow version: ?format=mermaid (flowchart, the default) |
// mermaid-state | bpmn | dot (Graphviz) | json (round-trippable), and ?version=N
// (defaults to the latest).
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
	case "dot", "graphviz":
		body, contentType, filename = export.DOT(ver.Graph), "text/vnd.graphviz; charset=utf-8", fv.Slug+".dot"
	case "json":
		js, err := export.JSON(flowExport(fv, ver))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		body, contentType, filename = js, "application/json; charset=utf-8", fv.Slug+".json"
	default:
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("unknown export format %q (mermaid|mermaid-state|bpmn|dot|json)", format))
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
	rec = s.maskRecord(r.Context(), id, rec) // mask PII in the exported data/output too
	steps := make([]export.RunStep, 0, len(rec.Nodes))
	for _, n := range rec.Nodes {
		steps = append(steps, export.RunStep{NodeID: n.NodeID, Type: string(n.Type)})
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "mermaid"
	}
	var body, contentType, filename string
	switch format {
	case "mermaid", "sequence":
		body, contentType, filename = export.MermaidSequence(rec.Slug, steps, rec.Status), "text/plain; charset=utf-8", rec.DecisionID+"-trace.mmd"
	case "dot", "graphviz":
		body, contentType, filename = export.RunDOT(rec.Slug, steps, rec.Status), "text/vnd.graphviz; charset=utf-8", rec.DecisionID+"-trace.dot"
	case "json":
		js, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		body, contentType, filename = string(js)+"\n", "application/json; charset=utf-8", rec.DecisionID+".json"
	default:
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("unknown export format %q (mermaid|dot|json)", format))
		return
	}
	writeExport(w, contentType, filename, body)
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

// flowExport builds the portable JSON form from a flow + the chosen version.
func flowExport(fv flows.FlowView, ver flows.VersionView) export.FlowExport {
	return export.FlowExport{
		Slug:        fv.Slug,
		Name:        fv.Name,
		Version:     ver.Version,
		Etag:        ver.Etag,
		Graph:       ver.Graph,
		InputSchema: ver.InputSchema,
	}
}

func writeExport(w http.ResponseWriter, contentType, filename, body string) {
	httpx.Download(w, contentType, filename, body)
}
