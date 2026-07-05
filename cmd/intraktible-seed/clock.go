// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"sync"
	"time"
)

// scriptedClock is the seed's time source: the schedule Set()s it forward before
// each action, and every read auto-advances it by a few milliseconds so multi-event
// operations (a decide's started → node → terminal chain) carry realistic
// intra-request timing and non-zero durations. It never moves backwards — the
// schedule is sorted, so a regression would be a seeder bug.
type scriptedClock struct {
	mu    sync.Mutex
	t     time.Time
	tick  int
	scale time.Duration // multiplies the auto steps (per-flow latency shaping)
}

// autoSteps is the per-read advance cycle (ms): small, deterministic jitter so
// node evaluations inside one decide are milliseconds apart, like a real run.
var autoSteps = []time.Duration{
	3 * time.Millisecond, 5 * time.Millisecond, 2 * time.Millisecond,
	7 * time.Millisecond, 4 * time.Millisecond, 6 * time.Millisecond,
}

func newScriptedClock(start time.Time) *scriptedClock {
	return &scriptedClock{t: start.UTC(), scale: 1}
}

// SetScale multiplies the per-read auto steps while a flow with heavier nodes
// (connectors, models, an LLM call) is deciding, so recorded durations track
// each graph's real cost profile.
func (c *scriptedClock) SetScale(scale int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if scale < 1 {
		scale = 1
	}
	c.scale = time.Duration(scale)
}

// Now returns the scripted time and advances it by the next auto step.
func (c *scriptedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.t
	c.t = c.t.Add(autoSteps[c.tick%len(autoSteps)] * c.scale)
	c.tick++
	return t
}

// Set moves the clock forward to t; a t at or behind the current scripted time is
// ignored (the auto-advance may have nudged past a same-instant schedule entry).
func (c *scriptedClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.After(c.t) {
		c.t = t.UTC()
	}
}

// Advance moves the clock forward by d — the scripted provider uses it so an LLM
// round-trip consumes believable wall-clock time inside a decide or agent run.
func (c *scriptedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
