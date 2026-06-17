// SPDX-License-Identifier: AGPL-3.0-or-later

package assertions

// StreamAssertions is the event stream for a flow's assertion set.
const StreamAssertions = "decision.assertions"

// TypeSet records a replacement of a flow's assertion cases.
const TypeSet = "decision.assertions_set"

// Set is the (whole) set of test cases for a flow.
type Set struct {
	FlowID string `json:"flow_id"`
	Cases  []Case `json:"cases"`
}
