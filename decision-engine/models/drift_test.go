// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestBucket(t *testing.T) {
	cases := map[float64]int{-0.5: 0, 0: 0, 0.05: 0, 0.1: 1, 0.55: 5, 0.999: 9, 1.0: 9, 1.5: 9}
	for p, want := range cases {
		if got := bucket(p); got != want {
			t.Errorf("bucket(%v) = %d, want %d", p, got, want)
		}
	}
}

func TestBucketDefensive(t *testing.T) {
	// A non-finite or out-of-range probability must yield a valid index, never a
	// negative/overflowing one (int(NaN) is a huge negative on amd64/arm64) — else
	// Hist[idx]++ would panic the projector on a poison event.
	for _, p := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1e308, 1e308, -0.5, 2.0} {
		idx := bucket(p)
		if idx < 0 || idx >= driftBuckets {
			t.Fatalf("bucket(%v) = %d, out of [0,%d)", p, idx, driftBuckets)
		}
	}
}

func TestPSI(t *testing.T) {
	base := Histogram{10, 10, 10, 10, 10, 10, 10, 10, 10, 10}

	// Identical distribution → ~0.
	if psi, ok := PSI(base, base); !ok || math.Abs(psi) > 1e-9 {
		t.Fatalf("identical PSI = %v (ok=%v), want ~0", psi, ok)
	}

	// A big shift → a large PSI well past the 0.25 "significant" threshold.
	shifted := Histogram{0, 0, 0, 0, 0, 0, 0, 0, 0, 100}
	psi, ok := PSI(base, shifted)
	if !ok || psi <= 0.25 {
		t.Fatalf("shifted PSI = %v (ok=%v), want > 0.25", psi, ok)
	}

	// Empty either side → not computable.
	if _, ok := PSI(Histogram{}, base); ok {
		t.Fatal("empty baseline should be non-computable")
	}
}

func TestDriftProjectorAlertResolve(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	key := store.Key("demo", "main", "risk")
	seedStats(t, s, ModelStats{Org: "demo", Workspace: "main", Name: "risk"})

	apply := func(typ string, payload any) {
		t.Helper()
		b, _ := json.Marshal(payload)
		if err := (DriftProjector{}).Apply(ctx, eventlog.Envelope{
			Org: "demo", Workspace: "main", Type: typ, Time: time.Now().UTC(), Payload: b,
		}, s); err != nil {
			t.Fatalf("apply %s: %v", typ, err)
		}
	}

	apply(events.TypeModelDriftAlerted, events.ModelDriftAlerted{Name: "risk", PSI: 0.5, Threshold: 0.25})
	st, _, _ := store.GetDoc[ModelStats](ctx, s, StatsCollection, key)
	if !st.Alerting {
		t.Fatal("alerted event should flip Alerting true")
	}

	apply(events.TypeModelDriftResolved, events.ModelDriftResolved{Name: "risk"})
	st, _, _ = store.GetDoc[ModelStats](ctx, s, StatsCollection, key)
	if st.Alerting {
		t.Fatal("resolved event should flip Alerting false")
	}
}

func TestDriftProjectorRejectsAlertForMissingStats(t *testing.T) {
	payload, err := json.Marshal(events.ModelDriftAlerted{Name: "missing", PSI: 0.5, Threshold: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	err = (DriftProjector{}).Apply(context.Background(), eventlog.Envelope{
		Seq: 7, Org: "demo", Workspace: "main", Type: events.TypeModelDriftAlerted,
		Time: time.Now().UTC(), Payload: payload,
	}, store.NewMemory())
	if err == nil {
		t.Fatal("alert transition for missing stats should fail loudly")
	}
}

func TestModelVersionCohortResetsAndReplays(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "builder"}
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	appendEvent := func(stream, typ string, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := log.Append(ctx, eventlog.Envelope{
			Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
			Stream: stream, Type: typ, Time: at, Payload: raw,
		}); err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Second)
	}
	define := func() {
		appendEvent(events.StreamModels, events.TypeModelDefined, events.ModelDefined{
			Name: "risk", Spec: json.RawMessage(`{"kind":"logistic","coefficients":{"x":1}}`),
		})
	}
	predict := func(decisionID, nodeID string, probability float64) {
		appendEvent(events.StreamDecisions, events.TypeNodeEvaluated, events.NodeEvaluated{
			DecisionID: decisionID, NodeID: nodeID, NodeType: events.NodePredict,
			Output: json.RawMessage(
				fmt.Sprintf(`{"risk":{"model":"risk","probability":%v}}`, probability),
			),
		})
	}
	outcome := func(version int, decisionID, nodeID string, probability, label float64) {
		appendEvent(events.StreamModels, events.TypeModelOutcomeRecorded, events.ModelOutcomeRecorded{
			Name: "risk", ModelVersion: version, Probability: probability, Label: label,
			DecisionID: decisionID, NodeID: nodeID,
		})
	}

	define()
	predict("d1", "p1", 0.9)
	outcome(1, "d1", "p1", 0.9, 1)
	define() // v2 must retire every v1 prediction, baseline, and actual counter.
	predict("d2", "p2", 0.2)
	outcome(2, "d2", "p2", 0.2, 0)
	// A cross-replica stale append remains visible as excluded evidence, never v2 data.
	outcome(1, "d1-late", "p1", 0.9, 1)

	rebuild := func() ModelStats {
		t.Helper()
		st := store.NewMemory()
		if _, err := projection.New(log, st, DriftProjector{}).RebuildTo(ctx, 0); err != nil {
			t.Fatal(err)
		}
		got, ok, err := store.GetDoc[ModelStats](
			ctx, st, StatsCollection, store.Key(id.Org, id.Workspace, "risk"),
		)
		if err != nil || !ok {
			t.Fatalf("read rebuilt stats: found=%v err=%v", ok, err)
		}
		return got
	}
	first := rebuild()
	if first.ModelVersion != 2 || first.Count != 1 || first.ActualCount != 1 ||
		first.Actuals[2].Neg != 1 || first.ExcludedActualCount != 1 {
		t.Fatalf("v2 cohort = %+v", first)
	}
	if again := rebuild(); !reflect.DeepEqual(first, again) {
		t.Fatalf("replay differs:\nfirst=%+v\nagain=%+v", first, again)
	}
}
