// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// slugPattern constrains a flow slug to a URL-safe form: it appears in the
// decide path, so it must be lowercase letters, digits, and hyphens.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// CreateFlow is the command to register a new flow.
type CreateFlow struct {
	Slug string
	Name string
}

// Validate fails loudly on a malformed slug or empty name.
func (c CreateFlow) Validate() error {
	if !slugPattern.MatchString(c.Slug) {
		return fmt.Errorf("decision-engine: invalid slug %q (lowercase letters, digits, hyphens)", c.Slug)
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("decision-engine: flow name is required")
	}
	return nil
}

// PublishVersion is the command to publish a new immutable version of a flow.
type PublishVersion struct {
	FlowID      string
	Graph       events.Graph
	InputSchema json.RawMessage
}

// Validate requires a target flow and a structurally valid graph.
func (c PublishVersion) Validate() error {
	if strings.TrimSpace(c.FlowID) == "" {
		return errors.New("decision-engine: flow_id is required")
	}
	return ValidateGraph(c.Graph)
}
