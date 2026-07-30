// SPDX-License-Identifier: AGPL-3.0-or-later

package experiments

import "time"

// Stream is the authoritative experiment, exposure, and analysis fact stream.
const Stream = "decision.experiments"

const (
	TypeCreated          = "decision.experiment.created"
	TypeUpdated          = "decision.experiment.updated"
	TypeLaunchRequested  = "decision.experiment.launch_requested"
	TypeLaunchApproved   = "decision.experiment.launch_approved"
	TypeLaunchRejected   = "decision.experiment.launch_rejected"
	TypeStarted          = "decision.experiment.started"
	TypePaused           = "decision.experiment.paused"
	TypeResumed          = "decision.experiment.resumed"
	TypeCompleted        = "decision.experiment.completed"
	TypeCancelled        = "decision.experiment.cancelled"
	TypeExposureRecorded = "decision.experiment.exposure_recorded"
)

// Created establishes cohort one in draft.
type Created struct {
	ExperimentID string `json:"experiment_id"`
	Spec         Spec   `json:"spec"`
}

// Updated replaces draft configuration and creates the named cohort.
type Updated struct {
	ExperimentID string `json:"experiment_id"`
	Cohort       int    `json:"cohort"`
	Spec         Spec   `json:"spec"`
}

// LaunchRequested is the maker side of production experiment launch.
type LaunchRequested struct {
	ExperimentID string `json:"experiment_id"`
	RequestID    string `json:"request_id"`
	Cohort       int    `json:"cohort"`
}

// LaunchApproved is the checker decision that starts production traffic.
type LaunchApproved struct {
	ExperimentID string `json:"experiment_id"`
	RequestID    string `json:"request_id"`
	Cohort       int    `json:"cohort"`
	Reason       string `json:"reason,omitempty"`
}

// LaunchRejected returns the same cohort to draft.
type LaunchRejected struct {
	ExperimentID string `json:"experiment_id"`
	RequestID    string `json:"request_id"`
	Cohort       int    `json:"cohort"`
	Reason       string `json:"reason,omitempty"`
}

// Transition records a non-production start or a lifecycle state change.
type Transition struct {
	ExperimentID string `json:"experiment_id"`
	Cohort       int    `json:"cohort"`
	Reason       string `json:"reason,omitempty"`
}

// ExposureRecorded proves the assigned flow treatment was reached. It is
// idempotent per decision and carries the exact immutable cohort/version.
type ExposureRecorded struct {
	ExperimentID string    `json:"experiment_id"`
	Cohort       int       `json:"cohort"`
	DecisionID   string    `json:"decision_id"`
	FlowID       string    `json:"flow_id"`
	Environment  string    `json:"environment"`
	ArmKey       string    `json:"arm_key"`
	ArmName      string    `json:"arm_name"`
	ArmKind      ArmKind   `json:"arm_kind"`
	Version      int       `json:"version"`
	SubjectHash  string    `json:"subject_hash"`
	ReachedAt    time.Time `json:"reached_at"`
}
