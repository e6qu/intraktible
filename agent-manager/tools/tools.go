// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tools provides Toolbox implementations the Agent Manager uses to
// execute an agent's declared tools during a tool-calling run. The reference
// toolbox exposes Context Layer connectors as callable tools: an agent that
// declares a connector's name as a tool can fetch from it mid-run, and the
// tool-calling loop records every call.
package tools

import (
	"context"
	"encoding/json"

	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/identity"
)

// connectorFetcher is the slice of the Context Layer connector provider this
// package needs (satisfied by connectors.Provider) — declared locally so this
// package does not import the connectors package.
type connectorFetcher interface {
	Fetch(ctx context.Context, id identity.Identity, connector string, params json.RawMessage) (json.RawMessage, error)
}

// ConnectorToolbox exposes Context Layer connectors as agent tools: a tool's name
// is the connector's name, and calling it fetches from that connector with the
// model-supplied arguments as params. Whether the named connector exists is
// resolved at call time (a missing connector is a recorded tool error fed back to
// the model), so Spec accepts any non-empty name.
type ConnectorToolbox struct {
	Fetcher connectorFetcher
}

// genericParams lets the model pass any JSON object as the connector params.
var genericParams = json.RawMessage(`{"type":"object"}`)

// Spec describes a connector tool to the provider.
func (t ConnectorToolbox) Spec(name string) (ai.Tool, bool) {
	if t.Fetcher == nil || name == "" {
		return ai.Tool{}, false
	}
	return ai.Tool{
		Name:        name,
		Description: "Fetch data from the '" + name + "' Context Layer connector. Arguments are the connector's params.",
		Parameters:  genericParams,
	}, true
}

// Call invokes the named connector with the model-supplied arguments.
func (t ConnectorToolbox) Call(ctx context.Context, id identity.Identity, name string, args json.RawMessage) (json.RawMessage, error) {
	return t.Fetcher.Fetch(ctx, id, name, args)
}
