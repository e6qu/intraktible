// SPDX-License-Identifier: AGPL-3.0-or-later

// Package domain is the hello feature's functional core: pure command validation
// and event construction, with no I/O. It must stay deterministic.
package domain

import (
	"errors"
	"strings"

	"github.com/e6qu/intraktible/hello/events"
)

// SayHello is the command to record a greeting.
type SayHello struct {
	Name string
}

// Validate fails loudly on bad input rather than coercing it.
func (c SayHello) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("hello: name is required")
	}
	return nil
}

// Record maps a valid command to its event (pure).
func Record(c SayHello) events.HelloRecorded {
	return events.HelloRecorded{Name: strings.TrimSpace(c.Name)}
}
