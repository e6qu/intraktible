// SPDX-License-Identifier: AGPL-3.0-or-later

package export_test

import (
	"encoding/xml"
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
