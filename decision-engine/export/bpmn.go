// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// BPMN renders the flow graph as BPMN 2.0 XML with BPMNDI diagram-interchange
// coordinates (from a layered auto-layout), so the result both validates as BPMN
// and opens, laid out, in bpmn.io / Camunda. Node types map to the closest BPMN
// element (start/end events, gateways, business-rule/service/script/user tasks);
// edges become sequence flows, with branch labels carried as the flow name.
func BPMN(g events.Graph, flowName string) string {
	const (
		procID = "Process_1"
		defsID = "Definitions_1"
	)
	pos := bpmnLayout(g)
	ids := assignIDs(g.Nodes, g.Edges, bpmnID)

	// Only edges between declared nodes become sequence flows: a dangling edge would
	// emit a sequenceFlow (and a zero-coordinate waypoint) referencing a flowNode that
	// has no shape, which BPMN tools reject.
	edges := soundEdges(g)
	type flow struct{ id, from, to, name string }
	flows := make([]flow, 0, len(edges))
	incoming := map[string][]string{}
	outgoing := map[string][]string{}
	for i, e := range edges {
		fid := fmt.Sprintf("flow_%d", i+1)
		flows = append(flows, flow{id: fid, from: e.From, to: e.To, name: e.Branch})
		outgoing[e.From] = append(outgoing[e.From], fid)
		incoming[e.To] = append(incoming[e.To], fid)
	}

	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<bpmn:definitions ` +
		`xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL" ` +
		`xmlns:bpmndi="http://www.omg.org/spec/BPMN/20100524/DI" ` +
		`xmlns:dc="http://www.omg.org/spec/DD/20100524/DC" ` +
		`xmlns:di="http://www.omg.org/spec/DD/20100524/DI" ` +
		`id="` + defsID + `" targetNamespace="http://intraktible/bpmn">` + "\n")

	fmt.Fprintf(&b, "  <bpmn:process id=\"%s\" name=\"%s\" isExecutable=\"false\">\n", procID, attr(flowName))
	for _, n := range g.Nodes {
		el := bpmnElement(n.Type)
		id := ids[n.ID]
		fmt.Fprintf(&b, "    <bpmn:%s id=\"%s\" name=\"%s\">\n", el, id, attr(displayName(n)))
		for _, f := range incoming[n.ID] {
			fmt.Fprintf(&b, "      <bpmn:incoming>%s</bpmn:incoming>\n", f)
		}
		for _, f := range outgoing[n.ID] {
			fmt.Fprintf(&b, "      <bpmn:outgoing>%s</bpmn:outgoing>\n", f)
		}
		fmt.Fprintf(&b, "    </bpmn:%s>\n", el)
	}
	for _, f := range flows {
		if f.name != "" {
			fmt.Fprintf(&b, "    <bpmn:sequenceFlow id=\"%s\" sourceRef=\"%s\" targetRef=\"%s\" name=\"%s\" />\n",
				f.id, ids[f.from], ids[f.to], attr(f.name))
		} else {
			fmt.Fprintf(&b, "    <bpmn:sequenceFlow id=\"%s\" sourceRef=\"%s\" targetRef=\"%s\" />\n",
				f.id, ids[f.from], ids[f.to])
		}
	}
	b.WriteString("  </bpmn:process>\n")

	// --- diagram interchange (layout) ---
	b.WriteString(`  <bpmndi:BPMNDiagram id="BPMNDiagram_1">` + "\n")
	fmt.Fprintf(&b, "    <bpmndi:BPMNPlane id=\"BPMNPlane_1\" bpmnElement=\"%s\">\n", procID)
	for _, n := range g.Nodes {
		bx := pos[n.ID]
		id := ids[n.ID]
		fmt.Fprintf(&b, "      <bpmndi:BPMNShape id=\"Shape_%s\" bpmnElement=\"%s\">\n", id, id)
		fmt.Fprintf(&b, "        <dc:Bounds x=\"%d\" y=\"%d\" width=\"%d\" height=\"%d\" />\n", bx.x, bx.y, bx.w, bx.h)
		b.WriteString("      </bpmndi:BPMNShape>\n")
	}
	for _, f := range flows {
		s, t := pos[f.from], pos[f.to]
		fmt.Fprintf(&b, "      <bpmndi:BPMNEdge id=\"Edge_%s\" bpmnElement=\"%s\">\n", f.id, f.id)
		fmt.Fprintf(&b, "        <di:waypoint x=\"%d\" y=\"%d\" />\n", s.x+s.w, s.y+s.h/2)
		fmt.Fprintf(&b, "        <di:waypoint x=\"%d\" y=\"%d\" />\n", t.x, t.y+t.h/2)
		b.WriteString("      </bpmndi:BPMNEdge>\n")
	}
	b.WriteString("    </bpmndi:BPMNPlane>\n")
	b.WriteString("  </bpmndi:BPMNDiagram>\n")
	b.WriteString("</bpmn:definitions>\n")
	return b.String()
}

// box is a laid-out node's top-left position and size.
type box struct{ x, y, w, h int }

// bpmnLayout assigns each node a layer (longest path from a root, via Kahn) and
// positions it in a left-to-right grid — a simple deterministic layout that BPMN
// tools render directly (and can re-auto-layout if desired).
func bpmnLayout(g events.Graph) map[string]box {
	const colW, rowH, originX, originY = 180, 110, 60, 60

	known := make(map[string]events.Node, len(g.Nodes))
	order := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		known[n.ID] = n
		order = append(order, n.ID)
	}
	adj := map[string][]string{}
	indeg := map[string]int{}
	for _, id := range order {
		indeg[id] = 0
	}
	for _, e := range g.Edges {
		if _, ok := known[e.From]; !ok {
			continue
		}
		if _, ok := known[e.To]; !ok {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
		indeg[e.To]++
	}
	layer := map[string]int{}
	queue := []string{}
	for _, id := range order {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, v := range adj[u] {
			if layer[u]+1 > layer[v] {
				layer[v] = layer[u] + 1
			}
			indeg[v]--
			if indeg[v] == 0 {
				queue = append(queue, v)
			}
		}
	}
	byLayer := map[int][]string{}
	maxLayer := 0
	for _, id := range order { // stable order within a layer
		l := layer[id]
		byLayer[l] = append(byLayer[l], id)
		if l > maxLayer {
			maxLayer = l
		}
	}
	out := make(map[string]box, len(order))
	for l := 0; l <= maxLayer; l++ {
		for i, id := range byLayer[l] {
			w, h := bpmnSize(known[id].Type)
			out[id] = box{x: originX + l*colW, y: originY + i*rowH, w: w, h: h}
		}
	}
	return out
}

// bpmnElement maps a node type to its BPMN element local name.
func bpmnElement(t events.NodeType) string {
	switch t {
	case events.NodeInput:
		return "startEvent"
	case events.NodeOutput:
		return "endEvent"
	case events.NodeSplit:
		return "exclusiveGateway"
	case events.NodeRule, events.NodeDecisionTable, events.NodeScorecard, events.NodeMatrix2D:
		return "businessRuleTask"
	case events.NodeAI, events.NodeConnect, events.NodePredict:
		return "serviceTask"
	case events.NodeCode:
		return "scriptTask"
	case events.NodeManualReview:
		return "userTask"
	default:
		return "task" // assignment and any future type
	}
}

// bpmnSize is the rendered size for a node type (events small, gateways diamond).
func bpmnSize(t events.NodeType) (w, h int) {
	switch t {
	case events.NodeInput, events.NodeOutput:
		return 36, 36
	case events.NodeSplit:
		return 50, 50
	default:
		return 100, 80
	}
}

// displayName is a node's label: its name, or its id when unnamed.
func displayName(n events.Node) string {
	if strings.TrimSpace(n.Name) != "" {
		return n.Name
	}
	return n.ID
}

// bpmnID coerces an id into a valid XML NCName (BPMN ids are NCNames). Distinct ids
// that coerce to the same NCName are kept distinct by assignIDs' uniqueness pass.
func bpmnID(id string) string {
	var b strings.Builder
	for i, r := range id {
		ok := r == '_' || r == '-' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			b.WriteByte('_')
			continue
		}
		if i == 0 && r >= '0' && r <= '9' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// attr escapes a string for use in an XML attribute value.
func attr(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
