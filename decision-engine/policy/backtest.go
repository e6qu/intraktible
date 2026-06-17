// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"encoding/json"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

// Distribution counts how a policy disposes a dataset (failed = the flow run
// errored, so no disposition could be assigned).
type Distribution struct {
	Approve int `json:"approve"`
	Decline int `json:"decline"`
	Refer   int `json:"refer"`
	Failed  int `json:"failed"`
}

// Flip is one record whose disposition differs between the evaluated and compare
// policies (only present in compare mode).
type Flip struct {
	Index     int    `json:"index"`
	Evaluated string `json:"evaluated"`
	Compare   string `json:"compare"`
}

// BacktestSummary is the headline of a disposition backtest. Evaluated is the
// distribution of the policy under test; Compare (when diffing) is the other
// version's distribution.
type BacktestSummary struct {
	Total     int           `json:"total"`
	Evaluated Distribution  `json:"evaluated"`
	Compare   *Distribution `json:"compare,omitempty"`
	Flipped   int           `json:"flipped,omitempty"`
}

// BacktestReport is a disposition backtest: how the policy would dispose a
// dataset, and (in compare mode) how that shifts versus another policy.
type BacktestReport struct {
	Summary BacktestSummary `json:"summary"`
	Flips   []Flip          `json:"flips,omitempty"`
}

// maxFlips caps the returned flipped-record sample.
const maxFlips = 200

// Backtest replays a dataset through the flow graph (pure: no recording, no I/O —
// like the flow backtest) and applies the evaluated policy to each output,
// returning the disposition distribution. When compare is non-nil it also disposes
// each row with that policy and reports the rows whose disposition flips.
func Backtest(g events.Graph, dataset []map[string]any, evaluated Spec, compare *Spec) BacktestReport {
	rep := BacktestReport{Summary: BacktestSummary{Total: len(dataset)}}
	if compare != nil {
		rep.Summary.Compare = &Distribution{}
	}
	for i, input := range dataset {
		run := domain.Execute(g, cloneInput(input))
		if run.Status != domain.StatusCompleted {
			rep.Summary.Evaluated.Failed++
			if compare != nil {
				rep.Summary.Compare.Failed++
			}
			continue
		}
		b := dispositionOf(evaluated, run.Output)
		tally(&rep.Summary.Evaluated, b)
		if compare != nil {
			c := dispositionOf(*compare, run.Output)
			tally(rep.Summary.Compare, c)
			if b != c {
				rep.Summary.Flipped++
				if len(rep.Flips) < maxFlips {
					rep.Flips = append(rep.Flips, Flip{Index: i, Evaluated: b, Compare: c})
				}
			}
		}
	}
	return rep
}

// dispositionOf applies a policy to an output, treating a non-evaluable policy as
// a referral (consistent with the decide path).
func dispositionOf(s Spec, output map[string]any) string {
	out, err := s.Apply(output)
	if err != nil {
		return Refer
	}
	return out.Disposition
}

func tally(d *Distribution, disp string) {
	switch disp {
	case Approve:
		d.Approve++
	case Decline:
		d.Decline++
	default:
		d.Refer++
	}
}

// cloneInput deep-copies an input map so Execute's in-place mutation of one run
// never leaks into another (a JSON round-trip is the simplest correct deep copy).
func cloneInput(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}
