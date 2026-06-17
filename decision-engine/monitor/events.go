// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

// StreamMonitors is the event stream for monitor definitions.
const StreamMonitors = "decision.monitors"

// Event type identifiers.
const (
	TypeDefined = "decision.monitor_defined"
	TypeDeleted = "decision.monitor_deleted"
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
