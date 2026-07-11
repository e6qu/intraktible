// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// round2 rounds a similarity score to 2 decimals for a stable, human-readable result.
func round2(x float64) float64 { return math.Round(x*100) / 100 }

// --- Sanctions / PEP screening connector ---
//
// A deterministic, in-process name-screening connector: it fuzzy-matches a subject
// name against an operator-supplied watchlist (OFAC SDN, EU/UN consolidated, a PEP
// list) and returns the matches above a threshold. Unlike the HTTP connectors it
// reaches no network — the watchlist is the config — so it is pure and replayable,
// and it works in the wasm build. The match is a token-set Jaccard similarity with an
// exact/subset boost, the standard cheap approach for sanctions name screening.

// defaultScreeningThreshold flags a watchlist entry when the name similarity is at
// least this high (0..1). Tuned so an exact or subset-of-tokens match always flags
// while unrelated names do not.
const defaultScreeningThreshold = 0.85

type watchEntry struct {
	Name    string `json:"name"`
	List    string `json:"list"`              // e.g. OFAC-SDN, EU, UN, PEP
	Program string `json:"program,omitempty"` // sanctions program / reason
}

type sanctionsConfig struct {
	Watchlist []watchEntry `json:"watchlist"`
	Threshold float64      `json:"threshold"` // 0..1; 0 → defaultScreeningThreshold
}

type sanctionsConnector struct {
	entries   []watchEntry
	threshold float64
}

func newSanctions(config json.RawMessage) (sanctionsConnector, error) {
	var cfg sanctionsConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return sanctionsConnector{}, fmt.Errorf("context-layer: sanctions connector config: %w", err)
		}
	}
	if len(cfg.Watchlist) == 0 {
		return sanctionsConnector{}, fmt.Errorf("context-layer: sanctions connector needs a non-empty watchlist")
	}
	for i, e := range cfg.Watchlist {
		if strings.TrimSpace(e.Name) == "" {
			return sanctionsConnector{}, fmt.Errorf("context-layer: sanctions watchlist entry %d needs a name", i)
		}
		if strings.TrimSpace(e.List) == "" {
			return sanctionsConnector{}, fmt.Errorf("context-layer: sanctions watchlist entry %d needs a list", i)
		}
	}
	if cfg.Threshold < 0 || cfg.Threshold > 1 {
		return sanctionsConnector{}, fmt.Errorf("context-layer: sanctions threshold %v: want 0..1", cfg.Threshold)
	}
	thr := cfg.Threshold
	if thr == 0 {
		thr = defaultScreeningThreshold
	}
	return sanctionsConnector{entries: cfg.Watchlist, threshold: thr}, nil
}

// sanctionsMatch is one flagged watchlist hit.
type sanctionsMatch struct {
	Name    string  `json:"name"`
	List    string  `json:"list"`
	Program string  `json:"program,omitempty"`
	Score   float64 `json:"score"`
}

// Fetch screens params.name against the watchlist and returns the flagged matches
// (highest score first) plus how many entries were screened. Deterministic, so the
// recorded fetch replays identically.
func (c sanctionsConnector) Fetch(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Name string `json:"name"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("context-layer: sanctions connector params must be a JSON object: %w", err)
		}
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, fmt.Errorf("context-layer: sanctions connector needs a name to screen")
	}
	query := nameTokens(p.Name)
	matches := []sanctionsMatch{}
	for _, e := range c.entries {
		score := nameSimilarity(query, nameTokens(e.Name))
		if score >= c.threshold {
			matches = append(matches, sanctionsMatch{Name: e.Name, List: e.List, Program: e.Program, Score: round2(score)})
		}
	}
	// Highest score first; ties broken by name for a stable, deterministic order.
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Name < matches[j].Name
	})
	return json.Marshal(map[string]any{
		"query":    p.Name,
		"matched":  len(matches) > 0,
		"matches":  matches,
		"screened": len(c.entries),
	})
}

// nameTokens normalizes a name to a set of lowercase alphanumeric tokens, so
// punctuation, case, and word order don't defeat a match.
func nameTokens(name string) map[string]struct{} {
	toks := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	set := make(map[string]struct{}, len(toks))
	for _, t := range toks {
		set[t] = struct{}{}
	}
	return set
}

// nameSimilarity is the token-set Jaccard similarity, boosted to 1.0 when one name's
// tokens are a subset of the other's (so "John Doe" matches "John Michael Doe" and
// vice versa — a common alias/middle-name case in screening).
func nameSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	if inter == len(a) || inter == len(b) {
		return 1
	}
	union := len(a) + len(b) - inter
	return float64(inter) / float64(union)
}
