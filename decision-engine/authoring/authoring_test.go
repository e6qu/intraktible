// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	enginecommand "github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/privacy"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestCanonicalFlowIsByteStableAndImportIsIdempotent(t *testing.T) {
	graph := flowWithMiddle(events.Node{
		ID: "rule", Type: events.NodeRule,
		Config: json.RawMessage(
			`{ "rules": [{ "then": [{ "expr": "'APPROVE'", "target": "decision" }], "when": "score >= 600" }] }`,
		),
		Position: &events.NodePosition{X: 812, Y: 94},
	})
	// Deliberately reverse representational order and vary JSON whitespace.
	graph.Nodes[0], graph.Nodes[2] = graph.Nodes[2], graph.Nodes[0]
	graph.Edges[0], graph.Edges[1] = graph.Edges[1], graph.Edges[0]
	asset := CanonicalFlow{
		FormatVersion: CanonicalFormatV1, Kind: CanonicalKindFlow,
		Slug: "credit", Name: " Credit decision ",
		Graph: graph, InputSchema: json.RawMessage(`{ "type": "object" }`),
	}
	first, err := MarshalCanonicalFlow(asset)
	if err != nil {
		t.Fatalf("marshal canonical flow: %v", err)
	}
	var roundTrip CanonicalFlow
	if err := json.Unmarshal(first, &roundTrip); err != nil {
		t.Fatalf("decode canonical flow: %v", err)
	}
	second, err := MarshalCanonicalFlow(roundTrip)
	if err != nil {
		t.Fatalf("re-marshal canonical flow: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical round trip changed bytes:\n%s\n%s", first, second)
	}
	for _, node := range roundTrip.Graph.Nodes {
		if node.Position != nil {
			t.Fatalf("canonical source retained layout position: %+v", node)
		}
	}

	ctx := context.Background()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "ci"}
	handler := NewHandler(log, st, enginecommand.NewHandler(log))
	handler.newID = sequenceIDs("draft-1")
	imported, err := handler.ImportCanonicalFlow(ctx, id, asset, "repository-commit-1")
	if err != nil {
		t.Fatalf("import canonical flow: %v", err)
	}
	retry, err := handler.ImportCanonicalFlow(ctx, id, asset, "repository-commit-1")
	if err != nil {
		t.Fatalf("retry canonical flow: %v", err)
	}
	if retry.FlowID != imported.FlowID || retry.DraftID != imported.DraftID ||
		retry.Event.Seq != imported.Event.Seq {
		t.Fatalf("retry = %+v, want original %+v", retry, imported)
	}
	changed := asset
	changed.Name = "Different request"
	if _, err := handler.ImportCanonicalFlow(
		ctx, id, changed, "repository-commit-1",
	); err == nil {
		t.Fatal("idempotency key accepted different canonical content")
	}
}

func TestCanonicalSubflowsUsePortableSlugsAndResolveWorkspaceIDs(t *testing.T) {
	workspaceGraph := flowWithMiddle(subflowNode("shared", "component-local-id", 3))
	canonicalGraph, err := canonicalComponentReferences(
		workspaceGraph,
		map[string]string{"component-local-id": "affordability-gate"},
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := MarshalCanonicalFlow(CanonicalFlow{
		FormatVersion: CanonicalFormatV1,
		Kind:          CanonicalKindFlow,
		Slug:          "portable-flow",
		Name:          "Portable flow",
		Graph:         canonicalGraph,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(document, []byte("component-local-id")) ||
		!bytes.Contains(document, []byte(`"component_slug": "affordability-gate"`)) {
		t.Fatalf("canonical component reference is workspace-bound:\n%s", document)
	}
	resolved, report, err := workspaceComponentReferences(
		canonicalGraph,
		map[string]string{"affordability-gate": "target-workspace-id"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rewrites) != 1 ||
		report.Rewrites[0].From != "affordability-gate" ||
		report.Rewrites[0].To != "target-workspace-id" {
		t.Fatalf("migration report = %#v", report)
	}
	var config SubflowConfig
	if err := json.Unmarshal(resolved.Nodes[1].Config, &config); err != nil ||
		config.ComponentID != "target-workspace-id" ||
		config.ComponentSlug != "" ||
		config.Version != 3 {
		t.Fatalf("resolved component config = %+v, err %v", config, err)
	}

	workspaceBound := CanonicalFlow{
		FormatVersion: CanonicalFormatV1,
		Kind:          CanonicalKindFlow,
		Slug:          "not-portable",
		Name:          "Not portable",
		Graph:         workspaceGraph,
	}
	if _, err := MarshalCanonicalFlow(workspaceBound); err == nil {
		t.Fatal("canonical flow accepted a workspace-local component_id")
	}
}

func TestCanonicalExportRejectsWorkspaceClassifiedFixtures(t *testing.T) {
	graph := flowWithMiddle(events.Node{
		ID: "rule", Type: events.NodeRule,
		Config: json.RawMessage(`{
			"fixture": {
				"SSN": "123-45-6789",
				"amount": 100
			}
		}`),
	})
	if err := rejectSensitiveFixtures(
		graph, privacy.FieldSet([]string{"ssn"}),
	); err == nil {
		t.Fatal("canonical export accepted a workspace-classified fixture")
	}
	if err := rejectSensitiveFixtures(
		graph, privacy.FieldSet([]string{"email"}),
	); err != nil {
		t.Fatalf("unclassified fixture rejected: %v", err)
	}
}

func TestReviewTextRejectsPIIAndAcceptsEvidenceReferences(t *testing.T) {
	if err := (CheckInput{
		Name: "assertions", Status: CheckPassed,
		Evidence: "customer SSN 123-45-6789",
	}).validate(); err == nil {
		t.Fatal("changeset check accepted raw PII")
	}
	if err := (ChangeSetInput{
		DraftID: "draft", DraftRevision: 1, Title: "Change",
		Rationale: "contact applicant@example.test",
	}).validate(); err == nil {
		t.Fatal("changeset rationale accepted raw PII")
	}
	if err := (ReviewInput{
		Decision: ReviewApprove, Reason: "call +1 (212) 555-0100",
	}).validate(); err == nil {
		t.Fatal("review reason accepted raw PII")
	}
	if err := (CheckInput{
		Name: "assertions", Status: CheckPassed,
		Evidence: "assertions/run-42: 24 of 24 passed",
	}).validate(); err != nil {
		t.Fatalf("immutable evidence reference rejected: %v", err)
	}
}

func TestCompileExpandsPinnedNestedComponentsAndRejectsCycles(t *testing.T) {
	leaf := ComponentVersion{
		ComponentID: "leaf", Version: 1, Etag: "leaf-etag",
		SourceGraph: componentGraph(events.Node{ID: "rule", Type: events.NodeRule}),
	}
	nested := ComponentVersion{
		ComponentID: "nested", Version: 2, Etag: "nested-etag",
		SourceGraph: componentGraph(subflowNode("leaf-ref", "leaf", 1)),
	}
	catalog := testResolver{
		"leaf@1":   leaf,
		"nested@2": nested,
	}
	source := flowWithMiddle(subflowNode("risk", "nested", 2))
	compiled, dependencies, err := Compile(source, catalog)
	if err != nil {
		t.Fatalf("compile nested component: %v", err)
	}
	if len(dependencies) != 2 ||
		dependencies[0].ComponentID != "leaf" ||
		dependencies[1].ComponentID != "nested" {
		t.Fatalf("dependencies = %#v, want exact sorted transitive pins", dependencies)
	}
	for _, node := range compiled.Nodes {
		if node.Type == events.NodeSubflow {
			t.Fatalf("compiled graph retained authoring-only node %#v", node)
		}
	}
	if err := domain.ValidateGraph(compiled); err != nil {
		t.Fatalf("compiled runtime graph: %v", err)
	}

	cyclic := testResolver{
		"a@1": {
			ComponentID: "a", Version: 1, Etag: "a",
			SourceGraph: componentGraph(subflowNode("b-ref", "b", 1)),
		},
		"b@1": {
			ComponentID: "b", Version: 1, Etag: "b",
			SourceGraph: componentGraph(subflowNode("a-ref", "a", 1)),
		},
	}
	if _, _, err := Compile(
		flowWithMiddle(subflowNode("a", "a", 1)), cyclic,
	); err == nil {
		t.Fatal("component cycle compiled successfully")
	}

	invalid := testResolver{
		"invalid@1": {
			ComponentID: "invalid", Version: 1, Etag: "invalid",
			SourceGraph: componentGraph(events.Node{
				ID: "rule", Type: events.NodeRule,
				Config: json.RawMessage(`{"when":"true"}`),
			}),
		},
	}
	if _, _, err := Compile(
		flowWithMiddle(subflowNode("invalid", "invalid", 1)), invalid,
	); err == nil {
		t.Fatal("component with invalid runtime node configuration compiled successfully")
	}
}

func TestComponentCompatibilityProtectsInputsOutputsAndRecordsBreakingMigration(t *testing.T) {
	oldInput := json.RawMessage(`{
		"type":"object",
		"properties":{"score":{"type":"number"}},
		"required":["score"],
		"additionalProperties":false
	}`)
	oldOutput := json.RawMessage(`{
		"type":"object",
		"properties":{"eligible":{"type":"boolean"},"reason":{"type":"string"}},
		"required":["eligible"]
	}`)
	compatibleInput := json.RawMessage(`{
		"type":"object",
		"properties":{"score":{"type":"number"},"segment":{"type":"string"}},
		"required":["score"],
		"additionalProperties":false
	}`)
	compatibleOutput := json.RawMessage(`{
		"type":"object",
		"properties":{"eligible":{"type":"boolean"},"reason":{"type":"string"},"band":{"type":"string"}},
		"required":["eligible","band"]
	}`)
	report, err := AssessComponentCompatibility(
		1, 2, oldInput, oldOutput, compatibleInput, compatibleOutput,
	)
	if err != nil || report.Status != CompatibilityCompatible {
		t.Fatalf("compatible report = %+v, err %v", report, err)
	}

	breakingInput := json.RawMessage(`{
		"type":"object",
		"properties":{"score":{"type":"number"},"country":{"type":"string"}},
		"required":["score","country"],
		"additionalProperties":false
	}`)
	breakingOutput := json.RawMessage(`{
		"type":"object",
		"properties":{"eligible":{"type":"string"}},
		"required":[]
	}`)
	report, err = AssessComponentCompatibility(
		1, 2, oldInput, oldOutput, breakingInput, breakingOutput,
	)
	if err != nil || report.Status != CompatibilityIncompatible || len(report.Issues) < 3 {
		t.Fatalf("breaking report = %+v, err %v", report, err)
	}

	ctx := context.Background()
	log := eventlog.NewMemory()
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "owner"}
	handler := NewHandler(log, store.NewMemory(), enginecommand.NewHandler(log))
	handler.newID = sequenceIDs("component", "upgrade-draft")
	componentID, _, err := handler.CreateComponent(
		ctx, id, ComponentInput{Slug: "eligibility", Name: "Eligibility"}, "create-component",
	)
	if err != nil {
		t.Fatal(err)
	}
	graph := componentGraph(events.Node{ID: "rule", Type: events.NodeRule})
	if _, _, _, err := handler.PublishComponent(ctx, id, componentID, ComponentVersionInput{
		Graph: graph, InputSchema: oldInput, OutputSchema: oldOutput,
	}, "publish-v1"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := handler.PublishComponent(ctx, id, componentID, ComponentVersionInput{
		Graph: graph, InputSchema: compatibleInput, OutputSchema: compatibleOutput,
	}, "publish-v2"); err != nil {
		t.Fatalf("publish compatible v2: %v", err)
	}
	if _, _, _, err := handler.PublishComponent(ctx, id, componentID, ComponentVersionInput{
		Graph: graph, InputSchema: breakingInput, OutputSchema: breakingOutput,
	}, "publish-v3"); err == nil {
		t.Fatal("breaking component version published without acknowledgement")
	} else {
		var compatibilityErr *CompatibilityError
		if !errors.As(err, &compatibilityErr) ||
			compatibilityErr.Report.Status != CompatibilityIncompatible {
			t.Fatalf("breaking publish error = %#v", err)
		}
	}
	version, _, _, err := handler.PublishComponent(ctx, id, componentID, ComponentVersionInput{
		Graph: graph, InputSchema: breakingInput, OutputSchema: breakingOutput,
		AllowBreaking: true, BreakingChangeReason: "Consumers must supply country and read boolean v1.",
	}, "publish-v3-ack")
	if err != nil || version != 3 {
		t.Fatalf("acknowledged breaking version = %d, err %v", version, err)
	}

	engine := enginecommand.NewHandler(log)
	flowID, _, err := engine.CreateFlow(
		ctx, id, domain.CreateFlow{Slug: "consumer", Name: "Consumer"},
	)
	if err != nil {
		t.Fatal(err)
	}
	source := flowWithMiddle(subflowNode("shared", componentID, 1))
	catalog, err := handler.foldComponents(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	compiled, dependencies, err := Compile(source, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.AppendJSON(
		ctx, log, id.Org, id.Workspace, id.Actor,
		events.StreamFlows, events.TypeFlowVersionPublished, time.Now().UTC(),
		events.FlowVersionPublished{
			FlowID: flowID, Version: 1, Etag: "reviewed",
			Graph: compiled, SourceGraph: &source, Dependencies: dependencies,
			ChangeSetID: "reviewed", DraftID: "source", DraftRevision: 1,
		},
	); err != nil {
		t.Fatal(err)
	}
	upgrades, err := handler.CreateComponentUpgradeDrafts(
		ctx, id, componentID, ComponentUpgradeInput{
			FromVersion: 1, ToVersion: 2, FlowIDs: []string{flowID},
		}, "upgrade-key",
	)
	if err != nil || len(upgrades) != 1 {
		t.Fatalf("compatible upgrade drafts = %+v, err %v", upgrades, err)
	}
	draft, found, err := handler.foldDraft(ctx, id, upgrades[0].DraftID)
	if err != nil || !found {
		t.Fatalf("read upgrade draft: found %v err %v", found, err)
	}
	var upgradedConfig SubflowConfig
	if err := json.Unmarshal(draft.Graph.Nodes[1].Config, &upgradedConfig); err != nil ||
		upgradedConfig.Version != 2 {
		t.Fatalf("upgraded direct pin = %+v err %v", upgradedConfig, err)
	}
	if _, err := handler.CreateComponentUpgradeDrafts(
		ctx, id, componentID, ComponentUpgradeInput{
			FromVersion: 1, ToVersion: 3, FlowIDs: []string{flowID},
		}, "breaking-upgrade",
	); err == nil {
		t.Fatal("automatic upgrade accepted an incompatible target")
	}
}

func TestAuthoringCreatesAreIdempotentAndRejectChangedRetries(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	engine := enginecommand.NewHandler(log)
	handler := NewHandler(log, store.NewMemory(), engine)
	handler.newID = sequenceIDs("draft", "component", "changeset")
	flowID, _, err := engine.CreateFlow(
		ctx, id, domain.CreateFlow{Slug: "credit", Name: "Credit"},
	)
	if err != nil {
		t.Fatal(err)
	}
	graph := flowWithMiddle(events.Node{ID: "rule", Type: events.NodeRule})
	draftInput := DraftInput{FlowID: flowID, Title: "Draft", Graph: graph}
	draftID, draftEvent, err := handler.CreateDraft(ctx, id, draftInput, "draft-key")
	if err != nil {
		t.Fatal(err)
	}
	retryID, retryEvent, err := handler.CreateDraft(ctx, id, draftInput, "draft-key")
	if err != nil || retryID != draftID || retryEvent.Seq != draftEvent.Seq {
		t.Fatalf("draft retry = %q seq %d err %v", retryID, retryEvent.Seq, err)
	}
	changedDraft := draftInput
	changedDraft.Title = "Different"
	if _, _, err := handler.CreateDraft(ctx, id, changedDraft, "draft-key"); err == nil {
		t.Fatal("changed draft retry reused an idempotency key")
	}

	componentInput := ComponentInput{Slug: "score", Name: "Score"}
	componentID, componentEvent, err := handler.CreateComponent(
		ctx, id, componentInput, "component-key",
	)
	if err != nil {
		t.Fatal(err)
	}
	retryComponentID, retryComponentEvent, err := handler.CreateComponent(
		ctx, id, componentInput, "component-key",
	)
	if err != nil || retryComponentID != componentID ||
		retryComponentEvent.Seq != componentEvent.Seq {
		t.Fatalf(
			"component retry = %q seq %d err %v",
			retryComponentID, retryComponentEvent.Seq, err,
		)
	}
	componentGraph := componentGraph(events.Node{ID: "rule", Type: events.NodeRule})
	versionInput := ComponentVersionInput{Graph: componentGraph}
	version, _, versionEvent, err := handler.PublishComponent(
		ctx, id, componentID, versionInput, "version-key",
	)
	if err != nil {
		t.Fatal(err)
	}
	retryVersion, _, retryVersionEvent, err := handler.PublishComponent(
		ctx, id, componentID, versionInput, "version-key",
	)
	if err != nil || retryVersion != version || retryVersionEvent.Seq != versionEvent.Seq {
		t.Fatalf(
			"component version retry = %d seq %d err %v",
			retryVersion, retryVersionEvent.Seq, err,
		)
	}

	changeSetInput := ChangeSetInput{
		DraftID: draftID, DraftRevision: 1, Title: "Review",
	}
	changeSetID, changeSetEvent, err := handler.CreateChangeSet(
		ctx, id, changeSetInput, "changeset-key",
	)
	if err != nil {
		t.Fatal(err)
	}
	retryChangeSetID, retryChangeSetEvent, err := handler.CreateChangeSet(
		ctx, id, changeSetInput, "changeset-key",
	)
	if err != nil || retryChangeSetID != changeSetID ||
		retryChangeSetEvent.Seq != changeSetEvent.Seq {
		t.Fatalf(
			"changeset retry = %q seq %d err %v",
			retryChangeSetID, retryChangeSetEvent.Seq, err,
		)
	}
}

func TestDraftCreateClaimIsIdempotentAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	engineA := enginecommand.NewHandler(log)
	flowID, _, err := engineA.CreateFlow(
		ctx, id, domain.CreateFlow{Slug: "replicated", Name: "Replicated"},
	)
	if err != nil {
		t.Fatal(err)
	}
	engineB := enginecommand.NewHandler(log)
	first := NewHandler(log, st, engineA)
	first.newID = sequenceIDs("draft-from-first")
	second := NewHandler(log, st, engineB)
	second.newID = sequenceIDs("draft-from-second")
	input := DraftInput{
		FlowID: flowID, Title: "One logical draft",
		Graph: flowWithMiddle(events.Node{ID: "rule", Type: events.NodeRule}),
	}
	type result struct {
		id    string
		event eventlog.Envelope
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for _, handler := range []*Handler{first, second} {
		go func(handler *Handler) {
			ready.Done()
			<-start
			draftID, event, createErr := handler.CreateDraft(
				ctx, id, input, "same-lost-response-key",
			)
			results <- result{id: draftID, event: event, err: createErr}
		}(handler)
	}
	ready.Wait()
	close(start)
	left, right := <-results, <-results
	if left.err != nil || right.err != nil {
		t.Fatalf("replica results = (%v, %v)", left.err, right.err)
	}
	if left.id == "" || left.id != right.id || left.event.Seq != right.event.Seq {
		t.Fatalf("replica claims diverged: left=%+v right=%+v", left, right)
	}
	envelopes, err := log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, envelope := range envelopes {
		if envelope.Type == TypeDraftCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("draft-created events = %d, want 1", created)
	}
}

func TestDraftConflictChangeSetReviewPublishAndReplay(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	maker := identity.Identity{
		Org: "acme", Workspace: "risk", Actor: "maker",
	}
	checker := maker
	checker.Actor = "checker"
	engine := enginecommand.NewHandler(log).WithNow(clock)
	handler := NewHandler(log, st, engine).WithNow(clock)
	handler.newID = sequenceIDs(
		"component-1", "draft-1", "changeset-1",
	)

	flowID, _, err := engine.CreateFlow(
		ctx, maker, domain.CreateFlow{Slug: "credit", Name: "Credit"},
	)
	if err != nil {
		t.Fatalf("create flow: %v", err)
	}
	base := flowWithMiddle(events.Node{ID: "rule", Type: events.NodeRule})
	if _, _, _, err := engine.PublishVersion(
		ctx, maker, domain.PublishVersion{FlowID: flowID, Graph: base},
	); err != nil {
		t.Fatalf("publish base: %v", err)
	}

	componentID, _, err := handler.CreateComponent(
		ctx, maker, ComponentInput{Slug: "score-gate", Name: "Score gate"}, "component-create",
	)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}
	if componentID != "component-1" {
		t.Fatalf("component id = %q", componentID)
	}
	componentSource := componentGraph(events.Node{ID: "gate", Type: events.NodeRule})
	if _, _, _, err := handler.PublishComponent(
		ctx, maker, componentID, ComponentVersionInput{Graph: componentSource}, "component-v1",
	); err != nil {
		t.Fatalf("publish component: %v", err)
	}

	source := flowWithMiddle(subflowNode("gate", componentID, 1))
	draftID, _, err := handler.CreateDraft(ctx, maker, DraftInput{
		FlowID: flowID, BaseVersion: 1, Title: "Raise threshold", Graph: source,
	}, "draft-create")
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	updated := source
	updated.Nodes[1].Name = "Reusable score gate"
	revision, _, err := handler.SaveDraft(ctx, maker, draftID, SaveDraftInput{
		ExpectedRevision: 1, Title: "Raise threshold", Graph: updated,
	})
	if err != nil || revision != 2 {
		t.Fatalf("save draft = revision %d, err %v", revision, err)
	}
	if retryRevision, retryEvent, retryErr := handler.SaveDraft(
		ctx, maker, draftID, SaveDraftInput{
			ExpectedRevision: 1, Title: "Raise threshold", Graph: updated,
		},
	); retryErr != nil || retryRevision != 2 || retryEvent.ID != "" {
		t.Fatalf(
			"identical lost-response save retry = revision %d event %q err %v",
			retryRevision, retryEvent.ID, retryErr,
		)
	}
	_, _, err = handler.SaveDraft(ctx, checker, draftID, SaveDraftInput{
		ExpectedRevision: 1, Title: "Stale", Graph: source,
	})
	var conflict *RevisionConflict
	if !errors.As(err, &conflict) || conflict.Current.Revision != 2 {
		t.Fatalf("stale save error = %#v, want revision conflict with current snapshot", err)
	}

	changeSetID, _, err := handler.CreateChangeSet(ctx, maker, ChangeSetInput{
		DraftID: draftID, DraftRevision: 2, Title: "Raise threshold",
		RequiredChecks: []string{"flow-validation"}, Reviewers: []string{"checker"},
	}, "changeset-create")
	if err != nil {
		t.Fatalf("create changeset: %v", err)
	}
	// The review artifact owns revision 2. A later autosave must not mutate or
	// invalidate what the checker reviews and publishes.
	updated.Nodes[1].Name = "Later unreviewed edit"
	if revision, _, err := handler.SaveDraft(ctx, maker, draftID, SaveDraftInput{
		ExpectedRevision: 2, Title: "Next iteration", Graph: updated,
	}); err != nil || revision != 3 {
		t.Fatalf("save after changeset = revision %d, err %v", revision, err)
	}
	if _, err := handler.RecordCheck(ctx, maker, changeSetID, CheckInput{
		Name: "flow-validation", Status: CheckPassed, Evidence: "compiled and validated",
	}); err != nil {
		t.Fatalf("record check: %v", err)
	}
	if retryEvent, retryErr := handler.RecordCheck(ctx, maker, changeSetID, CheckInput{
		Name: "flow-validation", Status: CheckPassed, Evidence: "compiled and validated",
	}); retryErr != nil || retryEvent.ID != "" {
		t.Fatalf("identical check retry = event %q err %v", retryEvent.ID, retryErr)
	}
	if _, err := handler.SubmitChangeSet(ctx, maker, changeSetID); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if retryEvent, retryErr := handler.SubmitChangeSet(
		ctx, maker, changeSetID,
	); retryErr != nil || retryEvent.ID != "" {
		t.Fatalf("identical submit retry = event %q err %v", retryEvent.ID, retryErr)
	}
	if _, err := handler.ReviewChangeSet(ctx, maker, changeSetID, ReviewInput{
		Decision: ReviewApprove,
	}); err == nil {
		t.Fatal("maker approved their own changeset")
	}
	if _, err := handler.ReviewChangeSet(ctx, checker, changeSetID, ReviewInput{
		Decision: ReviewApprove, Reason: "validated",
	}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if retryEvent, retryErr := handler.ReviewChangeSet(
		ctx, checker, changeSetID, ReviewInput{
			Decision: ReviewApprove, Reason: "validated",
		},
	); retryErr != nil || retryEvent.ID != "" {
		t.Fatalf("identical review retry = event %q err %v", retryEvent.ID, retryErr)
	}
	version, _, _, err := handler.PublishChangeSet(ctx, checker, changeSetID)
	if err != nil || version != 2 {
		t.Fatalf("publish changeset = version %d, err %v", version, err)
	}
	// Idempotent recovery returns the same publication rather than minting v3.
	repeated, _, _, err := handler.PublishChangeSet(ctx, checker, changeSetID)
	if err != nil || repeated != version {
		t.Fatalf("repeat publish = version %d, err %v", repeated, err)
	}

	replayed := store.NewMemory()
	if _, err := projection.New(
		log, replayed, flows.Projector{}, Projector{},
	).RebuildTo(ctx, 0); err != nil {
		t.Fatalf("replay: %v", err)
	}
	flow, found, err := flows.Read(ctx, replayed, maker, flowID)
	if err != nil || !found {
		t.Fatalf("read replayed flow: found %v, err %v", found, err)
	}
	if flow.Latest != 2 || len(flow.Versions[1].Dependencies) != 1 ||
		flow.Versions[1].ChangeSetID != changeSetID ||
		flow.Versions[1].SourceGraph == nil {
		t.Fatalf("published lineage = %#v", flow.Versions[1])
	}
	if got := flow.Versions[1].SourceGraph.Nodes[1].Name; got != "Reusable score gate" {
		t.Fatalf("published source node name = %q, want pinned revision 2", got)
	}
	for _, node := range flow.Versions[1].Graph.Nodes {
		if node.Type == events.NodeSubflow {
			t.Fatalf("published runtime graph retained subflow %#v", node)
		}
	}
	changeSet, found, err := ReadChangeSet(ctx, replayed, maker, changeSetID)
	if err != nil || !found || changeSet.State != ChangeSetPublished ||
		changeSet.PublishedVersion != 2 {
		t.Fatalf("replayed changeset = %#v, found %v, err %v", changeSet, found, err)
	}
	consumers, err := ListConsumers(ctx, replayed, maker, componentID, 1)
	if err != nil || len(consumers) != 1 || consumers[0].ConsumerID != flowID {
		t.Fatalf("component consumers = %#v, err %v", consumers, err)
	}
	rebase := RebaseDraftInput{
		ExpectedRevision: 3, BaseVersion: 2,
		Title: "Next iteration", Graph: updated,
	}
	if revision, _, err := handler.RebaseDraft(
		ctx, maker, draftID, rebase,
	); err != nil || revision != 4 {
		t.Fatalf("rebase draft = revision %d, err %v", revision, err)
	}
	if revision, retryEvent, err := handler.RebaseDraft(
		ctx, maker, draftID, rebase,
	); err != nil || revision != 4 || retryEvent.ID != "" {
		t.Fatalf(
			"identical rebase retry = revision %d event %q err %v",
			revision, retryEvent.ID, err,
		)
	}
	if _, err := handler.ArchiveDraft(ctx, maker, draftID); err != nil {
		t.Fatalf("archive draft: %v", err)
	}
	if retryEvent, retryErr := handler.ArchiveDraft(
		ctx, maker, draftID,
	); retryErr != nil || retryEvent.ID != "" {
		t.Fatalf("identical archive retry = event %q err %v", retryEvent.ID, retryErr)
	}
	if _, err := handler.RetireComponent(ctx, maker, componentID); err != nil {
		t.Fatalf("retire component: %v", err)
	}
	if retryEvent, retryErr := handler.RetireComponent(
		ctx, maker, componentID,
	); retryErr != nil || retryEvent.ID != "" {
		t.Fatalf("identical retire retry = event %q err %v", retryEvent.ID, retryErr)
	}
}

func TestDraftAutosavesIncompleteTopologyButChangeSetRequiresValidGraph(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	engine := enginecommand.NewHandler(log)
	handler := NewHandler(log, st, engine)
	handler.newID = sequenceIDs("draft", "changeset")
	flowID, _, err := engine.CreateFlow(
		ctx, id, domain.CreateFlow{Slug: "credit", Name: "Credit"},
	)
	if err != nil {
		t.Fatalf("create flow: %v", err)
	}
	draftID, _, err := handler.CreateDraft(ctx, id, DraftInput{
		FlowID: flowID,
		Title:  "Work in progress",
		Graph: events.Graph{Nodes: []events.Node{
			{ID: "input", Type: events.NodeInput},
			{ID: "rule", Type: events.NodeRule},
		}},
	}, "draft-create")
	if err != nil {
		t.Fatalf("autosave incomplete draft: %v", err)
	}
	if _, _, err := handler.CreateChangeSet(ctx, id, ChangeSetInput{
		DraftID: draftID, DraftRevision: 1, Title: "Not ready",
	}, "changeset-create"); err == nil {
		t.Fatal("incomplete draft became a changeset")
	}
}

func TestPresenceIsDisposableAndExpires(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "ada"}
	engine := enginecommand.NewHandler(log)
	handler := NewHandler(log, st, engine).WithNow(func() time.Time { return now })
	handler.newID = sequenceIDs("draft")
	flowID, _, err := engine.CreateFlow(
		ctx, id, domain.CreateFlow{Slug: "risk", Name: "Risk"},
	)
	if err != nil {
		t.Fatalf("create flow: %v", err)
	}
	draftID, _, err := handler.CreateDraft(ctx, id, DraftInput{
		FlowID: flowID, Title: "Draft", Graph: flowWithMiddle(events.Node{
			ID: "rule", Type: events.NodeRule,
		}),
	}, "presence-draft")
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if _, err := handler.RenewPresence(
		ctx, id, draftID, "Ada", 1, "rule", 15*time.Second,
	); err != nil {
		t.Fatalf("renew presence: %v", err)
	}
	if active, err := handler.ListPresence(ctx, id, draftID); err != nil || len(active) != 1 {
		t.Fatalf("active presence = %#v, err %v", active, err)
	}
	now = now.Add(16 * time.Second)
	if active, err := handler.ListPresence(ctx, id, draftID); err != nil || len(active) != 0 {
		t.Fatalf("expired presence = %#v, err %v", active, err)
	}
	if count, err := handler.SweepPresence(ctx); err != nil || count != 1 {
		t.Fatalf("sweep = %d, err %v", count, err)
	}
	// Presence has no event and therefore cannot reappear from replay.
	replayed := store.NewMemory()
	if _, err := projection.New(log, replayed, Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	records, err := replayed.List(ctx, PresenceCollection, "")
	if err != nil || len(records) != 0 {
		t.Fatalf("replayed presence = %#v, err %v", records, err)
	}
}

func TestSchedulerRemindsReviewersAndArchivesOnlyUnprotectedStaleDrafts(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	engine := enginecommand.NewHandler(log).WithNow(clock)
	handler := NewHandler(log, st, engine).WithNow(clock)
	handler.newID = sequenceIDs("stale", "protected", "changeset")
	flowID, _, err := engine.CreateFlow(
		ctx, id, domain.CreateFlow{Slug: "credit", Name: "Credit"},
	)
	if err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, _, _, err := engine.PublishVersion(ctx, id, domain.PublishVersion{
		FlowID: flowID, Graph: flowWithMiddle(events.Node{ID: "rule", Type: events.NodeRule}),
	}); err != nil {
		t.Fatalf("publish base: %v", err)
	}
	staleID, _, err := handler.CreateDraft(ctx, id, DraftInput{
		FlowID: flowID, BaseVersion: 1, Title: "Abandoned",
		Graph: flowWithMiddle(events.Node{ID: "rule", Type: events.NodeRule}),
	}, "stale-draft")
	if err != nil {
		t.Fatalf("create stale draft: %v", err)
	}
	protectedID, _, err := handler.CreateDraft(ctx, id, DraftInput{
		FlowID: flowID, BaseVersion: 1, Title: "Under review",
		Graph: flowWithMiddle(events.Node{ID: "rule", Type: events.NodeRule}),
	}, "protected-draft")
	if err != nil {
		t.Fatalf("create protected draft: %v", err)
	}
	changeSetID, _, err := handler.CreateChangeSet(ctx, id, ChangeSetInput{
		DraftID: protectedID, DraftRevision: 1, Title: "Under review",
		Reviewers: []string{"checker"},
	}, "protected-changeset")
	if err != nil {
		t.Fatalf("create changeset: %v", err)
	}
	if _, err := handler.SubmitChangeSet(ctx, id, changeSetID); err != nil {
		t.Fatalf("submit changeset: %v", err)
	}
	if _, err := projection.New(
		log, st, flows.Projector{}, Projector{},
	).RebuildTo(ctx, 0); err != nil {
		t.Fatalf("project setup: %v", err)
	}

	now = now.Add(91 * 24 * time.Hour)
	scheduler := NewScheduler(handler, st).WithNow(clock)
	summary, err := scheduler.Tick(ctx)
	if err != nil {
		t.Fatalf("scheduler tick: %v", err)
	}
	if summary.Reminders != 1 || summary.Archived != 1 {
		t.Fatalf("scheduler summary = %+v", summary)
	}
	repeated, err := scheduler.Tick(ctx)
	if err != nil {
		t.Fatalf("repeat scheduler tick: %v", err)
	}
	if repeated.Reminders != 0 || repeated.Archived != 0 {
		t.Fatalf("repeat scheduler was not idempotent: %+v", repeated)
	}
	if _, err := projection.New(
		log, st, flows.Projector{}, Projector{},
	).RebuildTo(ctx, 0); err != nil {
		t.Fatalf("replay scheduler events: %v", err)
	}
	stale, found, err := ReadDraft(ctx, st, id, staleID)
	if err != nil || !found || stale.State != DraftStateArchived ||
		stale.ArchiveReason != "stale draft retention" {
		t.Fatalf("stale draft after retention = %+v, found=%v err=%v", stale, found, err)
	}
	protected, found, err := ReadDraft(ctx, st, id, protectedID)
	if err != nil || !found || protected.State != DraftStateActive {
		t.Fatalf("protected draft = %+v, found=%v err=%v", protected, found, err)
	}
}

type testResolver map[string]ComponentVersion

func (r testResolver) ResolveComponent(id string, version int) (ComponentVersion, bool) {
	component, ok := r[id+"@"+strconv.Itoa(version)]
	return component, ok
}

func subflowNode(id, componentID string, version int) events.Node {
	config, _ := json.Marshal(SubflowConfig{ComponentID: componentID, Version: version})
	return events.Node{ID: id, Type: events.NodeSubflow, Config: config}
}

func componentGraph(middle events.Node) events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			middle,
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{{From: "in", To: middle.ID}, {From: middle.ID, To: "out"}},
	}
}

func flowWithMiddle(middle events.Node) events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			{ID: "input", Type: events.NodeInput},
			middle,
			{ID: "output", Type: events.NodeOutput},
		},
		Edges: []events.Edge{{From: "input", To: middle.ID}, {From: middle.ID, To: "output"}},
	}
}

func sequenceIDs(ids ...string) func() string {
	index := 0
	return func() string {
		if index >= len(ids) {
			panic("test id sequence exhausted")
		}
		id := ids[index]
		index++
		return id
	}
}
