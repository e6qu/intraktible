// SPDX-License-Identifier: AGPL-3.0-or-later

// Package experiments implements governed, replayable champion/challenger
// experiments. Assignment is a pure function of the immutable cohort
// configuration and a stable subject key; exposures and outcomes are separate
// facts so assignment alone can never inflate sample sizes.
package experiments

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/entity"
	"github.com/expr-lang/expr"
)

// State is the governed lifecycle of an experiment cohort.
type State string

const (
	StateDraft         State = "draft"
	StatePendingLaunch State = "pending_launch"
	StateRunning       State = "running"
	StatePaused        State = "paused"
	StateCompleted     State = "completed"
	StateCancelled     State = "cancelled"
)

// Valid reports whether s is a supported experiment state.
func (s State) Valid() bool {
	switch s {
	case StateDraft, StatePendingLaunch, StateRunning, StatePaused, StateCompleted, StateCancelled:
		return true
	default:
		return false
	}
}

// ArmKind identifies the control and treatment arms without limiting an
// experiment to only one challenger.
type ArmKind string

const (
	ArmChampion   ArmKind = "champion"
	ArmChallenger ArmKind = "challenger"
)

// Valid reports whether k is a supported arm kind.
func (k ArmKind) Valid() bool { return k == ArmChampion || k == ArmChallenger }

// MetricKind determines how observations are summarized.
type MetricKind string

const (
	MetricBinary     MetricKind = "binary"
	MetricContinuous MetricKind = "continuous"
)

// Valid reports whether k is a supported metric kind.
func (k MetricKind) Valid() bool { return k == MetricBinary || k == MetricContinuous }

// Direction says whether larger or smaller metric values are preferred.
type Direction string

const (
	DirectionIncrease Direction = "increase"
	DirectionDecrease Direction = "decrease"
)

// Valid reports whether d is supported.
func (d Direction) Valid() bool { return d == DirectionIncrease || d == DirectionDecrease }

// Arm pins one treatment to an immutable published flow version. AllocationBPS
// is basis points and all arms must total exactly 10,000.
type Arm struct {
	Key           string  `json:"key"`
	Name          string  `json:"name"`
	Kind          ArmKind `json:"kind"`
	Version       int     `json:"version"`
	AllocationBPS int     `json:"allocation_bps"`
}

// Metric defines an outcome-backed KPI or guardrail. MaxRegression is the
// largest tolerated movement in the wrong direction for a guardrail.
type Metric struct {
	Key           string     `json:"key"`
	Name          string     `json:"name"`
	Kind          MetricKind `json:"kind"`
	Direction     Direction  `json:"direction"`
	MaxRegression float64    `json:"max_regression,omitempty"`
}

// Spec is the immutable configuration of one cohort. Updating a draft creates
// a new cohort number so old assignments, exposures, and outcomes never mix
// with materially different logic.
type Spec struct {
	Name                  string     `json:"name"`
	Hypothesis            string     `json:"hypothesis"`
	Owner                 string     `json:"owner"`
	FlowID                string     `json:"flow_id"`
	Environment           string     `json:"environment"`
	SubjectKeyExpression  string     `json:"subject_key_expression"`
	EligibilityExpression string     `json:"eligibility_expression,omitempty"`
	Salt                  string     `json:"salt"`
	Arms                  []Arm      `json:"arms"`
	PrimaryMetric         Metric     `json:"primary_metric"`
	Guardrails            []Metric   `json:"guardrails,omitempty"`
	MinimumSamplePerArm   int        `json:"minimum_sample_per_arm"`
	MinimumEffect         float64    `json:"minimum_effect"`
	Confidence            float64    `json:"confidence"`
	ObservationWindowDays int        `json:"observation_window_days"`
	StartAt               *time.Time `json:"start_at,omitempty"`
	StopAt                *time.Time `json:"stop_at,omitempty"`
}

// Validate checks the complete experiment contract.
func (s Spec) Validate() error {
	switch {
	case strings.TrimSpace(s.Name) == "":
		return fmt.Errorf("experiments: name is required")
	case len(s.Name) > 200:
		return fmt.Errorf("experiments: name exceeds 200 bytes")
	case strings.TrimSpace(s.Hypothesis) == "":
		return fmt.Errorf("experiments: hypothesis is required")
	case len(s.Hypothesis) > 4000:
		return fmt.Errorf("experiments: hypothesis exceeds 4000 bytes")
	case strings.TrimSpace(s.Owner) == "":
		return fmt.Errorf("experiments: owner is required")
	case strings.TrimSpace(s.FlowID) == "":
		return fmt.Errorf("experiments: flow_id is required")
	case s.Environment != "sandbox" && s.Environment != "staging" && s.Environment != "production":
		return fmt.Errorf("experiments: invalid environment %q", s.Environment)
	case strings.TrimSpace(s.SubjectKeyExpression) == "":
		return fmt.Errorf("experiments: subject_key_expression is required")
	case strings.TrimSpace(s.Salt) == "":
		return fmt.Errorf("experiments: salt is required")
	case len(s.Salt) > 256:
		return fmt.Errorf("experiments: salt exceeds 256 bytes")
	case len(s.Arms) < 2:
		return fmt.Errorf("experiments: at least two arms are required")
	case s.MinimumSamplePerArm < 2:
		return fmt.Errorf("experiments: minimum_sample_per_arm must be at least 2")
	case s.MinimumEffect < 0:
		return fmt.Errorf("experiments: minimum_effect cannot be negative")
	case s.Confidence < 0.8 || s.Confidence >= 1:
		return fmt.Errorf("experiments: confidence must be in [0.8,1)")
	case s.ObservationWindowDays < 0 || s.ObservationWindowDays > 3650:
		return fmt.Errorf("experiments: observation_window_days must be between 0 and 3650")
	case s.StartAt != nil && s.StopAt != nil && !s.StopAt.After(*s.StartAt):
		return fmt.Errorf("experiments: stop_at must be after start_at")
	}
	env := expressionEnvironment(map[string]any{}, entity.Ref{})
	if _, err := expr.Compile(
		s.SubjectKeyExpression, expr.Env(env), expr.AllowUndefinedVariables(), expr.DisableBuiltin("now"),
	); err != nil {
		return fmt.Errorf("experiments: subject_key_expression: %w", err)
	}
	if s.EligibilityExpression != "" {
		if _, err := expr.Compile(
			s.EligibilityExpression, expr.Env(env), expr.AllowUndefinedVariables(),
			expr.AsBool(), expr.DisableBuiltin("now"),
		); err != nil {
			return fmt.Errorf("experiments: eligibility_expression: %w", err)
		}
	}
	seen := make(map[string]bool, len(s.Arms))
	total, champions := 0, 0
	for i, arm := range s.Arms {
		if strings.TrimSpace(arm.Key) == "" || strings.TrimSpace(arm.Name) == "" {
			return fmt.Errorf("experiments: arm %d requires key and name", i)
		}
		if seen[arm.Key] {
			return fmt.Errorf("experiments: duplicate arm key %q", arm.Key)
		}
		seen[arm.Key] = true
		if !arm.Kind.Valid() {
			return fmt.Errorf("experiments: arm %q has invalid kind %q", arm.Key, arm.Kind)
		}
		if arm.Kind == ArmChampion {
			champions++
		}
		if arm.Version < 1 {
			return fmt.Errorf("experiments: arm %q version must be positive", arm.Key)
		}
		if arm.AllocationBPS <= 0 {
			return fmt.Errorf("experiments: arm %q allocation_bps must be positive", arm.Key)
		}
		total += arm.AllocationBPS
	}
	if champions != 1 {
		return fmt.Errorf("experiments: exactly one champion arm is required")
	}
	if total != 10_000 {
		return fmt.Errorf("experiments: arm allocations total %d basis points, want 10000", total)
	}
	if err := s.PrimaryMetric.validate(false); err != nil {
		return fmt.Errorf("experiments: primary_metric: %w", err)
	}
	metricKeys := map[string]bool{s.PrimaryMetric.Key: true}
	for i, guardrail := range s.Guardrails {
		if err := guardrail.validate(true); err != nil {
			return fmt.Errorf("experiments: guardrail %d: %w", i, err)
		}
		if metricKeys[guardrail.Key] {
			return fmt.Errorf("experiments: duplicate metric key %q", guardrail.Key)
		}
		metricKeys[guardrail.Key] = true
	}
	return nil
}

func (m Metric) validate(guardrail bool) error {
	switch {
	case strings.TrimSpace(m.Key) == "":
		return fmt.Errorf("key is required")
	case strings.TrimSpace(m.Name) == "":
		return fmt.Errorf("name is required")
	case !m.Kind.Valid():
		return fmt.Errorf("invalid kind %q", m.Kind)
	case !m.Direction.Valid():
		return fmt.Errorf("invalid direction %q", m.Direction)
	case guardrail && m.MaxRegression < 0:
		return fmt.Errorf("max_regression cannot be negative")
	default:
		return nil
	}
}

// Assignment is a version-pinned cohort decision. SubjectHash is retained
// instead of the raw subject key to minimize replicated personal data.
type Assignment struct {
	ExperimentID string  `json:"experiment_id"`
	Cohort       int     `json:"cohort"`
	ArmKey       string  `json:"arm_key"`
	ArmName      string  `json:"arm_name"`
	ArmKind      ArmKind `json:"arm_kind"`
	Version      int     `json:"version"`
	SubjectHash  string  `json:"subject_hash"`
}

// Assign evaluates eligibility and deterministically assigns a stable subject.
// eligible=false is not an error: the ordinary deployed champion serves it.
func Assign(spec Spec, experimentID string, cohort int, data map[string]any, ref entity.Ref) (Assignment, bool, error) {
	env := expressionEnvironment(data, ref)
	if spec.EligibilityExpression != "" {
		program, err := expr.Compile(spec.EligibilityExpression, expr.Env(env), expr.AsBool(), expr.DisableBuiltin("now"))
		if err != nil {
			return Assignment{}, false, fmt.Errorf("experiments: compile eligibility: %w", err)
		}
		value, err := expr.Run(program, env)
		if err != nil {
			return Assignment{}, false, fmt.Errorf("experiments: evaluate eligibility: %w", err)
		}
		eligible, ok := value.(bool)
		if !ok {
			return Assignment{}, false, fmt.Errorf("experiments: eligibility expression returned %T, want bool", value)
		}
		if !eligible {
			return Assignment{}, false, nil
		}
	}
	program, err := expr.Compile(spec.SubjectKeyExpression, expr.Env(env), expr.DisableBuiltin("now"))
	if err != nil {
		return Assignment{}, false, fmt.Errorf("experiments: compile subject key: %w", err)
	}
	value, err := expr.Run(program, env)
	if err != nil {
		return Assignment{}, false, fmt.Errorf("experiments: evaluate subject key: %w", err)
	}
	subject, err := stableSubject(value)
	if err != nil {
		return Assignment{}, false, err
	}
	digest := sha256.Sum256([]byte(spec.Salt + "\x00" + strconv.Itoa(cohort) + "\x00" + subject))
	// The modulo proves the value fits in int on every supported architecture.
	bucket := int(binary.BigEndian.Uint64(digest[:8]) % 10_000) // #nosec G115
	offset := 0
	for _, arm := range spec.Arms {
		offset += arm.AllocationBPS
		if bucket < offset {
			return Assignment{
				ExperimentID: experimentID, Cohort: cohort,
				ArmKey: arm.Key, ArmName: arm.Name, ArmKind: arm.Kind,
				Version: arm.Version, SubjectHash: hex.EncodeToString(digest[:16]),
			}, true, nil
		}
	}
	return Assignment{}, false, fmt.Errorf("experiments: allocation did not cover bucket %d", bucket)
}

func stableSubject(value any) (string, error) {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("experiments: subject key is empty")
		}
		return "s:" + v, nil
	case int:
		return "i:" + strconv.Itoa(v), nil
	case int64:
		return "i:" + strconv.FormatInt(v, 10), nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return "", fmt.Errorf("experiments: subject key is not finite")
		}
		return "n:" + strconv.FormatFloat(v, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("experiments: subject key expression returned %T, want string or number", value)
	}
}

func expressionEnvironment(data map[string]any, ref entity.Ref) map[string]any {
	env := make(map[string]any, len(data)+2)
	for key, value := range data {
		env[key] = value
	}
	env["data"] = data
	env["entity"] = map[string]any{"type": string(ref.Type), "id": string(ref.ID)}
	return env
}
