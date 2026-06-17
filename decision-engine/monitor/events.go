// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

// StreamMonitors is the event stream for monitor definitions.
const StreamMonitors = "decision.monitors"

// Event type identifiers.
const (
	TypeDefined          = "decision.monitor_defined"
	TypeDeleted          = "decision.monitor_deleted"
	TypeBaselineCaptured = "decision.monitor_baseline_captured"
	// Alert-transition events let a scheduled check notify only on the ok→firing
	// edge (and reset on firing→ok), instead of re-alerting every tick.
	TypeAlerted  = "decision.monitor_alerted"
	TypeResolved = "decision.monitor_resolved"
)

// Defined records a new monitor on a flow.
type Defined struct {
	MonitorID   string  `json:"monitor_id"`
	FlowID      string  `json:"flow_id"`
	Metric      string  `json:"metric"`
	Op          string  `json:"op"`
	Threshold   float64 `json:"threshold"`
	Description string  `json:"description,omitempty"`
}

// Deleted records a monitor's removal.
type Deleted struct {
	MonitorID string `json:"monitor_id"`
	FlowID    string `json:"flow_id"`
}

// BaselineCaptured records a flow's disposition distribution at a point in time —
// the reference that distribution_drift monitors measure against. Computed from
// the metrics in the shell (an effect), so it is replay-stable.
type BaselineCaptured struct {
	FlowID  string  `json:"flow_id"`
	Approve float64 `json:"approve"`
	Decline float64 `json:"decline"`
	Refer   float64 `json:"refer"`
	Total   int     `json:"total"`
}

// Alerted records that a monitor crossed into the firing state (and was notified).
type Alerted struct {
	MonitorID string `json:"monitor_id"`
	FlowID    string `json:"flow_id"`
}

// Resolved records that a previously-firing monitor returned to ok.
type Resolved struct {
	MonitorID string `json:"monitor_id"`
	FlowID    string `json:"flow_id"`
}
