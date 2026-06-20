// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// HTTPDoer is the subset of *http.Client the external-model path needs. The
// composition root supplies an egress-guarded client (the same SSRF protection the
// HTTP connector uses); a fake satisfies it in tests.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// maxExternalBody caps an external model-serving response read into memory.
const maxExternalBody = 1 << 20

// Collection is the read-model collection for model definitions.
const Collection = "decision_models"

// ModelView is the materialized read model for one model definition.
type ModelView struct {
	Org       string          `json:"org"`
	Workspace string          `json:"workspace"`
	Name      string          `json:"name"`
	Kind      ModelKind       `json:"kind"`
	Spec      json.RawMessage `json:"spec"`
	UpdatedAt string          `json:"updated_at"`
}

// Projector folds ModelDefined events into ModelView documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_models" }

// Collections lists the store collections this projector owns (reset on rebuild).
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains the model registry read model.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != events.TypeModelDefined {
		return nil
	}
	var p events.ModelDefined
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("models: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	// A ModelDefined event that can't be parsed is a projection failure, not a
	// blank field — DefineModel validates first, so this is defense in depth that
	// must still fail loudly rather than materialize a kind-less row.
	spec, err := ParseSpec(p.Spec)
	if err != nil {
		return fmt.Errorf("models: decode spec %s seq %d: %w", e.Type, e.Seq, err)
	}
	kind := spec.Kind
	v := ModelView{
		Org: e.Org, Workspace: e.Workspace,
		Name: p.Name, Kind: kind, Spec: p.Spec, UpdatedAt: e.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.Name), v)
}

// Read returns one model definition for id's tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, name string) (ModelView, bool, error) {
	return store.GetDoc[ModelView](ctx, s, Collection, store.Key(id.Org, id.Workspace, name))
}

// List returns the tenant's model definitions in name order.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]ModelView, error) {
	return store.QueryDocs(ctx, s, Collection, store.Key(id.Org, id.Workspace, ""),
		nil, func(a, b ModelView) bool { return a.Name < b.Name })
}

// Provider resolves and evaluates a registered model — the adapter the decision
// engine's Predict nodes call (it structurally satisfies the engine's ModelProvider
// port without the engine importing this package's command surface).
type Provider struct {
	Store store.Store
	// HTTP is the egress-guarded client for "external" (BYO served) models. When nil,
	// external models fail loudly; the in-process kinds never need it.
	HTTP HTTPDoer
}

// Predict resolves the named model from the registry and returns its prediction as
// JSON (the decision records it for replay). In-process kinds are evaluated purely;
// an "external" model is served over the egress-guarded HTTP client.
func (p Provider) Predict(ctx context.Context, id identity.Identity, model string, features map[string]any) (json.RawMessage, error) {
	mv, ok, err := Read(ctx, p.Store, id, model)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("models: unknown model %q", model)
	}
	spec, err := ParseSpec(mv.Spec)
	if err != nil {
		return nil, err
	}
	// Validate before evaluating: Validate is the only thing that guarantees a GBM
	// tree's children are non-nil, and (*Tree).eval dereferences them. DefineModel
	// validates on the write path, but a replay of a pre-validation event or a
	// future bulk-import must not be able to panic the evaluator with a nil child.
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("models: model %q has an invalid spec: %w", model, err)
	}
	if spec.Kind == KindExternal {
		return p.predictExternal(ctx, spec, features)
	}
	pred, err := Evaluate(spec, features)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pred)
}

// predictExternal POSTs the features to the model's serving endpoint and reads back
// a {score, probability?} prediction. The call goes through the egress-guarded
// client, so it inherits the same SSRF protection as the HTTP connector.
func (p Provider) predictExternal(ctx context.Context, spec Spec, features map[string]any) (json.RawMessage, error) {
	if p.HTTP == nil {
		return nil, fmt.Errorf("models: no HTTP client configured for external model serving")
	}
	body, err := json.Marshal(features)
	if err != nil {
		return nil, fmt.Errorf("models: marshal features: %w", err)
	}
	timeout := time.Duration(spec.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spec.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("models: external request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models: external model call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models: external model returned status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxExternalBody))
	if err != nil {
		return nil, fmt.Errorf("models: read external response: %w", err)
	}
	var pred Prediction
	if err := json.Unmarshal(raw, &pred); err != nil {
		return nil, fmt.Errorf("models: external response is not a {score,probability} prediction: %w", err)
	}
	// Reject a non-finite or out-of-range result up front (fail loudly) rather than
	// recording a NaN/Inf score or an out-of-[0,1] probability that would corrupt
	// downstream branching and the drift histogram.
	if math.IsNaN(pred.Score) || math.IsInf(pred.Score, 0) {
		return nil, fmt.Errorf("models: external model returned a non-finite score")
	}
	if pred.Probability != nil {
		p := *pred.Probability
		if math.IsNaN(p) || p < 0 || p > 1 {
			return nil, fmt.Errorf("models: external model returned probability %v outside [0,1]", p)
		}
	}
	return json.Marshal(pred)
}
