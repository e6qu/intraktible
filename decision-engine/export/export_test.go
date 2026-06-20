// SPDX-License-Identifier: AGPL-3.0-or-later

package export_test

import (
	"encoding/json"
	"encoding/xml"
	"reflect"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/export"
)

// sample is a small flow with a branch: input → split →(yes) approve, →(no) decline → output.
func sample() events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "s", Type: events.NodeSplit, Name: "score check"},
			{ID: "ai", Type: events.NodeAI, Name: "assess"},
			{ID: "mr", Type: events.NodeManualReview, Name: "review"},
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{
			{From: "in", To: "s"},
			{From: "s", To: "ai", Branch: "yes"},
			{From: "s", To: "mr", Branch: "no"},
			{From: "ai", To: "out"},
			{From: "mr", To: "out"},
		},
	}
}

func TestMermaidFlowchart(t *testing.T) {
	out := export.MermaidFlowchart(sample())
	for _, want := range []string{
		"flowchart TD",
		`s{"score check (split)"}`, // split → diamond, label = name (type)
		`in([`,                     // input → stadium
		`ai[(`,                     // ai → cylinder
		`mr{{`,                     // manual_review → hexagon
		"s -->|yes| ai",            // branch edge label
		"s -->|no| mr",
		"ai --> out",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("flowchart missing %q in:\n%s", want, out)
		}
	}
}

func TestMermaidState(t *testing.T) {
	out := export.MermaidState(sample())
	for _, want := range []string{
		"stateDiagram-v2",
		"[*] --> in",  // input from start
		"out --> [*]", // output to end
		"s --> ai : yes",
		"state \"score check (split)\" as s",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("state diagram missing %q in:\n%s", want, out)
		}
	}
}

func TestMermaidSequence(t *testing.T) {
	steps := []export.RunStep{{NodeID: "in", Type: "input"}, {NodeID: "s", Type: "split"}, {NodeID: "out", Type: "output"}}
	out := export.MermaidSequence("creditflow", steps, "completed")
	for _, want := range []string{
		"sequenceDiagram",
		"participant E as creditflow",
		"C->>E: decide",
		"Note over E: s (split)",
		"E-->>C: completed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("sequence diagram missing %q in:\n%s", want, out)
		}
	}
}

func TestDOT(t *testing.T) {
	out := export.DOT(sample())
	for _, want := range []string{
		"digraph flow {",
		"rankdir=TB;",
		`"in" [label="in (input)", shape=ellipse];`,
		`"s" [label="score check (split)", shape=diamond];`,
		`"ai" [label="assess (ai)", shape=cylinder];`,
		`"mr" [label="review (manual_review)", shape=hexagon];`,
		`"s" -> "ai" [label="yes"];`,
		`"ai" -> "out";`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("DOT missing %q in:\n%s", want, out)
		}
	}
}

func TestDOTQuotesSpecialChars(t *testing.T) {
	g := events.Graph{Nodes: []events.Node{{ID: `a"b`, Type: events.NodeOutput, Name: "x\ny"}}}
	out := export.DOT(g)
	if !strings.Contains(out, `"a\"b"`) {
		t.Fatalf("DOT did not escape a quote in the id:\n%s", out)
	}
	if strings.Contains(out, "x\ny") {
		t.Fatalf("DOT did not flatten the newline in the label:\n%s", out)
	}
}

func TestJSONRoundTrips(t *testing.T) {
	g := sample()
	in := export.FlowExport{
		Slug:        "credit",
		Name:        "Credit Flow",
		Version:     3,
		Etag:        "abc123",
		Graph:       g,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	out, err := export.JSON(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"slug": "credit"`) || !strings.Contains(out, `"version": 3`) {
		t.Fatalf("JSON missing metadata:\n%s", out)
	}
	// The exported graph round-trips back to the original (re-importable).
	var got export.FlowExport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(got.Graph, g) {
		t.Fatalf("graph did not round-trip: %+v != %+v", got.Graph, g)
	}
}

func TestRunDOT(t *testing.T) {
	steps := []export.RunStep{
		{NodeID: "in", Type: "input"},
		{NodeID: "s", Type: "split"},
		{NodeID: "out", Type: "output"},
	}
	out := export.RunDOT("creditflow", steps, "completed")
	for _, want := range []string{
		"digraph run {",
		`"__start" [label="decide: creditflow", shape=circle];`,
		`"s" [label="s (split)", shape=box];`,
		`"__end" [label="completed", shape=doublecircle];`,
		`"__start" -> "in";`,
		`"in" -> "s";`,
		`"out" -> "__end";`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("run DOT missing %q in:\n%s", want, out)
		}
	}
}

func TestBPMNIsWellFormedAndComplete(t *testing.T) {
	out := export.BPMN(sample(), "Credit Flow")

	// It must be well-formed XML.
	dec := xml.NewDecoder(strings.NewReader(out))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("BPMN is not well-formed XML: %v", err)
		}
	}

	for _, want := range []string{
		`<bpmn:definitions`,
		`<bpmn:process id="Process_1" name="Credit Flow"`,
		`<bpmn:startEvent id="in"`,      // input → start event
		`<bpmn:endEvent id="out"`,       // output → end event
		`<bpmn:exclusiveGateway id="s"`, // split → gateway
		`<bpmn:serviceTask id="ai"`,     // ai → service task
		`<bpmn:userTask id="mr"`,        // manual_review → user task
		`<bpmn:sequenceFlow id="flow_2" sourceRef="s" targetRef="ai" name="yes"`,
		`<bpmn:incoming>flow_1</bpmn:incoming>`,
		`<bpmndi:BPMNDiagram`, // DI present
		`<dc:Bounds x=`,       // node coordinates
		`<di:waypoint x=`,     // edge waypoints
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("BPMN missing %q in:\n%s", want, out)
		}
	}
}

func TestBPMNSanitizesIDs(t *testing.T) {
	g := events.Graph{
		Nodes: []events.Node{{ID: "1 weird/id", Type: events.NodeInput, Name: "Start"}},
	}
	out := export.BPMN(g, `name & <stuff>`)
	if !strings.Contains(out, `name="name &amp; &lt;stuff&gt;"`) {
		t.Fatalf("BPMN did not escape the process name:\n%s", out)
	}
	// The id is coerced to a valid NCName; the raw id is never used as an id attr.
	if !strings.Contains(out, `id="_1_weird_id"`) {
		t.Fatalf("BPMN did not sanitize the node id to an NCName:\n%s", out)
	}
	if strings.Contains(out, `id="1 weird/id"`) {
		t.Fatalf("BPMN used an invalid raw id as an id attribute:\n%s", out)
	}
}

// TestBPMNDisambiguatesCollidingIDs proves distinct node ids that coerce to the
// same NCName get unique ids, and the sequence-flow refs point at the right ones.
func TestBPMNDisambiguatesCollidingIDs(t *testing.T) {
	g := events.Graph{
		Nodes: []events.Node{
			{ID: "a/b", Type: events.NodeInput, Name: "first"},
			{ID: "a b", Type: events.NodeOutput, Name: "second"}, // also coerces to a_b
		},
		Edges: []events.Edge{{From: "a/b", To: "a b"}},
	}
	out := export.BPMN(g, "collide")

	if !strings.Contains(out, `id="a_b"`) || !strings.Contains(out, `id="a_b_2"`) {
		t.Fatalf("expected disambiguated ids a_b and a_b_2:\n%s", out)
	}
	// The flow must connect the two distinct elements, not alias them together.
	if !strings.Contains(out, `sourceRef="a_b" targetRef="a_b_2"`) {
		t.Fatalf("sequence flow refs did not track the disambiguated ids:\n%s", out)
	}
	// Every element/shape id must be unique (no duplicate NCName).
	ids := map[string]int{}
	for _, tok := range strings.Split(out, `id="`) {
		if i := strings.IndexByte(tok, '"'); i > 0 {
			ids[tok[:i]]++
		}
	}
	for id, n := range ids {
		if n > 1 {
			t.Fatalf("duplicate id %q appears %d times:\n%s", id, n, out)
		}
	}
}

// Two distinct node ids that coerce to the same Mermaid identifier must stay
// distinct in the output — otherwise the renderer merges them into one node and
// cross-wires every edge that referenced either.
func TestMermaidIDCollisionsStayDistinct(t *testing.T) {
	g := events.Graph{
		Nodes: []events.Node{
			{ID: "a.b", Type: events.NodeInput},
			{ID: "a/b", Type: events.NodeOutput},
		},
		Edges: []events.Edge{{From: "a.b", To: "a/b"}},
	}
	out := export.MermaidFlowchart(g)
	// Both sanitize to "a_b"; the uniqueness pass must suffix the second.
	if !strings.Contains(out, "a_b_2") {
		t.Fatalf("colliding ids were not disambiguated:\n%s", out)
	}
	// The single edge must connect the two distinct ids, not a self-loop.
	if strings.Contains(out, "a_b --> a_b\n") {
		t.Fatalf("colliding ids merged into a self-loop:\n%s", out)
	}
}

// An edge to a node that does not exist must not produce a sequenceFlow/waypoint
// referencing a missing element (which BPMN tools reject), and the BPMN must stay
// well-formed XML.
func TestBPMNDropsDanglingEdges(t *testing.T) {
	g := events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{
			{From: "in", To: "out"},
			{From: "in", To: "ghost"},    // dangling target
			{From: "phantom", To: "out"}, // dangling source
		},
	}
	out := export.BPMN(g, "dangling")
	if strings.Contains(out, "ghost") || strings.Contains(out, "phantom") {
		t.Fatalf("dangling endpoints leaked into BPMN:\n%s", out)
	}
	// Exactly one sound sequence flow survives.
	if n := strings.Count(out, "<bpmn:sequenceFlow "); n != 1 {
		t.Fatalf("want 1 sequence flow, got %d:\n%s", n, out)
	}
	if err := xml.Unmarshal([]byte(out), new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("BPMN is not well-formed XML: %v", err)
	}
}

// A predict node is an external ML call (like AI/Connect), so every exporter must
// render it as a service-task/cylinder, not the generic default task — otherwise the
// exported diagram silently mislabels it.
func TestPredictNodeExportsAsServiceCall(t *testing.T) {
	g := events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "p", Type: events.NodePredict, Name: "score"},
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{{From: "in", To: "p"}, {From: "p", To: "out"}},
	}
	if bpmn := export.BPMN(g, "f"); !strings.Contains(bpmn, `<bpmn:serviceTask id="p"`) {
		t.Fatalf("predict node should be a BPMN serviceTask:\n%s", bpmn)
	}
	if dot := export.DOT(g); !strings.Contains(dot, "shape=cylinder") {
		t.Fatalf("predict node should be a DOT cylinder:\n%s", dot)
	}
	if mer := export.MermaidFlowchart(g); !strings.Contains(mer, "[(") {
		t.Fatalf("predict node should be a Mermaid cylinder:\n%s", mer)
	}
}
