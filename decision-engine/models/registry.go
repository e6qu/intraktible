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

// PendingApproval is a model version awaiting a checker's decision (four-eyes).
type PendingApproval struct {
	RequestID   string `json:"request_id"`
	Version     int    `json:"version"`
	RequestedBy string `json:"requested_by"`
	RequestedAt string `json:"requested_at"`
}

// ValidationRecord is one piece of validation evidence for a model version.
type ValidationRecord struct {
	Version    int                `json:"version"`
	Dataset    string             `json:"dataset,omitempty"`
	Metrics    map[string]float64 `json:"metrics,omitempty"`
	Validator  string             `json:"validator,omitempty"`
	Notes      string             `json:"notes,omitempty"`
	Passed     bool               `json:"passed"`
	RecordedBy string             `json:"recorded_by,omitempty"`
	RecordedAt string             `json:"recorded_at,omitempty"`
}

// ModelView is the materialized read model for one model definition, including its
// governance state (four-eyes approval) and validation evidence.
type ModelView struct {
	Org       string          `json:"org"`
	Workspace string          `json:"workspace"`
	Name      string          `json:"name"`
	Kind      ModelKind       `json:"kind"`
	Spec      json.RawMessage `json:"spec"`
	// Owner is the actor who last defined the model — the model-owner proxy MRM
	// surfaces, mirroring flows/agents' PublishedBy. Derived from the event actor,
	// so it is populated retroactively for every prior ModelDefined on replay.
	Owner string `json:"owner,omitempty"`
	// Version counts ModelDefined events for this name. Each redefine bumps it, so an
	// approval granted to an earlier version no longer matches the current definition.
	Version int `json:"version"`
	// ApprovedVersion is the version a checker approved (0 = never approved). The model
	// is approved for serving only when ApprovedVersion == Version.
	ApprovedVersion int    `json:"approved_version"`
	ApprovedBy      string `json:"approved_by,omitempty"`
	ApprovedAt      string `json:"approved_at,omitempty"`
	// Pending is set while a version awaits a checker's decision.
	Pending     *PendingApproval   `json:"pending,omitempty"`
	Validations []ValidationRecord `json:"validations,omitempty"`
	UpdatedAt   string             `json:"updated_at"`
}

// Approved reports whether the model's current version has four-eyes approval — the
// gate a non-sandbox decision requires before serving a prediction from it.
func (v ModelView) Approved() bool {
	return v.Version > 0 && v.ApprovedVersion == v.Version
}

// Projector folds ModelDefined events into ModelView documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_models" }

// Collections lists the store collections this projector owns (reset on rebuild).
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains the model registry read model across definition, governance
// (four-eyes approval), and validation-evidence events.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeModelDefined:
		return applyDefined(ctx, e, s)
	case events.TypeModelApprovalRequested:
		return applyApprovalRequested(ctx, e, s)
	case events.TypeModelApprovalApproved:
		return applyApprovalApproved(ctx, e, s)
	case events.TypeModelApprovalRejected:
		return applyApprovalRejected(ctx, e, s)
	case events.TypeModelValidationRecorded:
		return applyValidationRecorded(ctx, e, s)
	default:
		return nil
	}
}

// ts formats an event time the way the read model stores timestamps.
func ts(e eventlog.Envelope) string { return e.Time.UTC().Format("2006-01-02T15:04:05Z07:00") }

func loadModel(ctx context.Context, s store.Store, e eventlog.Envelope, name string) (ModelView, error) {
	v, _, err := store.GetDoc[ModelView](ctx, s, Collection, store.Key(e.Org, e.Workspace, name))
	return v, err
}

func applyDefined(ctx context.Context, e eventlog.Envelope, s store.Store) error {
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
	v, err := loadModel(ctx, s, e, p.Name)
	if err != nil {
		return err
	}
	// Each redefine is a new version. A pending request for the prior version is moot;
	// a prior approval stays recorded but no longer matches Version, so Approved() is
	// false until the new version is re-approved — the "changed logic, re-review" rule.
	v.Org, v.Workspace, v.Name = e.Org, e.Workspace, p.Name
	v.Kind, v.Spec, v.Owner = spec.Kind, p.Spec, e.Actor
	v.Version++
	v.Pending = nil
	v.UpdatedAt = ts(e)
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.Name), v)
}

func applyApprovalRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ModelApprovalRequested
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("models: decode approval-requested seq %d: %w", e.Seq, err)
	}
	v, err := loadModel(ctx, s, e, p.Name)
	if err != nil {
		return err
	}
	// Ignore a request that no longer names the current version (a redefine raced it).
	if p.Version != v.Version {
		return nil
	}
	v.Pending = &PendingApproval{RequestID: p.RequestID, Version: p.Version, RequestedBy: e.Actor, RequestedAt: ts(e)}
	v.UpdatedAt = ts(e)
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.Name), v)
}

func applyApprovalApproved(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ModelApprovalApproved
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("models: decode approval-approved seq %d: %w", e.Seq, err)
	}
	v, err := loadModel(ctx, s, e, p.Name)
	if err != nil {
		return err
	}
	if v.Pending == nil || v.Pending.RequestID != p.RequestID {
		return nil
	}
	v.ApprovedVersion, v.ApprovedBy, v.ApprovedAt = p.Version, e.Actor, ts(e)
	v.Pending = nil
	v.UpdatedAt = ts(e)
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.Name), v)
}

func applyApprovalRejected(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ModelApprovalRejected
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("models: decode approval-rejected seq %d: %w", e.Seq, err)
	}
	v, err := loadModel(ctx, s, e, p.Name)
	if err != nil {
		return err
	}
	if v.Pending == nil || v.Pending.RequestID != p.RequestID {
		return nil
	}
	v.Pending = nil
	v.UpdatedAt = ts(e)
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.Name), v)
}

func applyValidationRecorded(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ModelValidationRecorded
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("models: decode validation seq %d: %w", e.Seq, err)
	}
	v, err := loadModel(ctx, s, e, p.Name)
	if err != nil {
		return err
	}
	v.Validations = append(v.Validations, ValidationRecord{
		Version: p.Version, Dataset: p.Dataset, Metrics: p.Metrics,
		Validator: p.Validator, Notes: p.Notes, Passed: p.Passed,
		RecordedBy: e.Actor, RecordedAt: ts(e),
	})
	v.UpdatedAt = ts(e)
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

// unknownRefError marks a lookup of a name this tenant never defined. The decide
// path recognises it structurally (BadProviderRef) and maps it to a caller error
// — a flow referencing a missing definition is fixable config, not a server fault.
type unknownRefError struct{ msg string }

func (e unknownRefError) Error() string        { return e.msg }
func (e unknownRefError) BadProviderRef() bool { return true }

// ApprovedForServing reports whether a model's current version has four-eyes
// approval — the gate a non-sandbox decision checks before serving its prediction.
// An unknown model is reported as an unknown reference (a fixable flow config error),
// not simply unapproved, so the caller can distinguish the two.
func (p Provider) ApprovedForServing(ctx context.Context, id identity.Identity, model string) (bool, error) {
	mv, ok, err := Read(ctx, p.Store, id, model)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, unknownRefError{msg: fmt.Sprintf("models: unknown model %q", model)}
	}
	return mv.Approved(), nil
}

func (p Provider) Predict(ctx context.Context, id identity.Identity, model string, features map[string]any) (json.RawMessage, error) {
	mv, ok, err := Read(ctx, p.Store, id, model)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, unknownRefError{msg: fmt.Sprintf("models: unknown model %q", model)}
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
	// Attach the model's input feature values so the drift projector can track
	// covariate drift (the recorded prediction is the only per-model signal it folds).
	// Evaluate already required these features to be present, so the lookups succeed.
	resp := map[string]any{"score": pred.Score}
	if pred.Probability != nil {
		resp["probability"] = *pred.Probability
	}
	if names := spec.FeatureNames(); len(names) > 0 {
		fv := make(map[string]float64, len(names))
		for _, name := range names {
			if x, err := feature(features, name); err == nil {
				fv[name] = x
			}
		}
		if len(fv) > 0 {
			resp["features"] = fv
		}
	}
	return json.Marshal(resp)
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
	pred, err := parseExternalPrediction(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pred)
}

// parseExternalPrediction decodes and validates a {score, probability?} response
// from an external (BYO served) model. It rejects malformed JSON, a non-finite
// score, and an out-of-[0,1] (or non-finite) probability up front (fail loudly)
// rather than recording a value that would corrupt downstream branching and the
// drift histogram. It is the trust boundary for an attacker-influenced HTTP body,
// so it must never panic — only ever return a prediction or an error.
func parseExternalPrediction(raw []byte) (Prediction, error) {
	var pred Prediction
	if err := json.Unmarshal(raw, &pred); err != nil {
		return Prediction{}, fmt.Errorf("models: external response is not a {score,probability} prediction: %w", err)
	}
	if math.IsNaN(pred.Score) || math.IsInf(pred.Score, 0) {
		return Prediction{}, fmt.Errorf("models: external model returned a non-finite score")
	}
	if pred.Probability != nil {
		p := *pred.Probability
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return Prediction{}, fmt.Errorf("models: external model returned probability %v outside [0,1]", p)
		}
	}
	return pred, nil
}
