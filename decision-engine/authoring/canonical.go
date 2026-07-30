// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/privacy"
)

const (
	// CanonicalFormatV1 is the first repository-stable authoring asset format.
	CanonicalFormatV1 = "intraktible.authoring/v1"
	CanonicalKindFlow = "flow"
)

// CanonicalFlow is a repository-stable flow source document. It deliberately
// excludes published version numbers, etags, generated layout coordinates, and
// deployment state: those belong to the target workspace and would make the
// same semantic source produce noisy diffs.
type CanonicalFlow struct {
	FormatVersion string          `json:"format_version"`
	Kind          string          `json:"kind"`
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Graph         events.Graph    `json:"graph"`
	InputSchema   json.RawMessage `json:"input_schema,omitempty"`
}

// CanonicalBundle is an atomic validation unit for repository-managed flow
// sources. Import still creates one independently reviewable draft per flow.
type CanonicalBundle struct {
	FormatVersion string          `json:"format_version"`
	Kind          string          `json:"kind"`
	Flows         []CanonicalFlow `json:"flows"`
}

// CanonicalRewrite is one deterministic target-workspace rewrite performed
// while importing portable repository source.
type CanonicalRewrite struct {
	Path   string `json:"path"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// CanonicalMigrationReport makes every target-specific rewrite visible to CI
// and reviewers. An empty report means the canonical source required no
// workspace-local identifier resolution.
type CanonicalMigrationReport struct {
	Rewrites []CanonicalRewrite `json:"rewrites"`
}

// DecodeCanonicalFlow rejects unknown fields and trailing documents so a typo
// in repository-managed source cannot be accepted as a different program than
// the author intended.
func DecodeCanonicalFlow(document []byte) (CanonicalFlow, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var asset CanonicalFlow
	if err := decoder.Decode(&asset); err != nil {
		return CanonicalFlow{}, fmt.Errorf("authoring: decode canonical flow: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CanonicalFlow{}, errors.New("authoring: canonical flow has trailing JSON content")
	}
	if _, err := NormalizeCanonicalFlow(asset); err != nil {
		return CanonicalFlow{}, err
	}
	return asset, nil
}

func (asset CanonicalFlow) validate() error {
	if asset.FormatVersion != CanonicalFormatV1 {
		return fmt.Errorf(
			"authoring: unsupported canonical format %q (want %q)",
			asset.FormatVersion, CanonicalFormatV1,
		)
	}
	if asset.Kind != CanonicalKindFlow {
		return fmt.Errorf("authoring: unsupported canonical kind %q", asset.Kind)
	}
	if !slugPattern.MatchString(asset.Slug) {
		return fmt.Errorf("authoring: invalid flow slug %q", asset.Slug)
	}
	if strings.TrimSpace(asset.Name) == "" {
		return errors.New("authoring: canonical flow name is required")
	}
	if len(asset.Description) > maxDescription {
		return fmt.Errorf(
			"authoring: description too long (%d > %d)",
			len(asset.Description), maxDescription,
		)
	}
	for _, node := range asset.Graph.Nodes {
		if node.Type != events.NodeSubflow {
			continue
		}
		var config SubflowConfig
		if err := json.Unmarshal(node.Config, &config); err != nil {
			return fmt.Errorf("authoring: canonical subflow node %q config: %w", node.ID, err)
		}
		if config.ComponentID != "" || strings.TrimSpace(config.ComponentSlug) == "" ||
			config.Version < 1 {
			return fmt.Errorf(
				"authoring: canonical subflow node %q requires portable component_slug "+
					"and an exact positive version; workspace component_id is not canonical",
				node.ID,
			)
		}
	}
	return validateDraftContent(asset.Name, asset.Graph, asset.InputSchema)
}

// NormalizeCanonicalFlow removes layout-only noise, normalizes embedded JSON,
// and orders graph members by stable semantic identity.
func NormalizeCanonicalFlow(asset CanonicalFlow) (CanonicalFlow, error) {
	if err := asset.validate(); err != nil {
		return CanonicalFlow{}, err
	}
	asset.Name = strings.TrimSpace(asset.Name)
	asset.Description = strings.TrimSpace(asset.Description)
	asset.Graph = cloneGraph(asset.Graph)
	for index := range asset.Graph.Nodes {
		asset.Graph.Nodes[index].Position = nil
		config, err := canonicalJSON(asset.Graph.Nodes[index].Config)
		if err != nil {
			return CanonicalFlow{}, fmt.Errorf(
				"authoring: canonical node %q config: %w",
				asset.Graph.Nodes[index].ID, err,
			)
		}
		asset.Graph.Nodes[index].Config = config
	}
	sort.Slice(asset.Graph.Nodes, func(i, j int) bool {
		return asset.Graph.Nodes[i].ID < asset.Graph.Nodes[j].ID
	})
	sort.Slice(asset.Graph.Edges, func(i, j int) bool {
		left, right := asset.Graph.Edges[i], asset.Graph.Edges[j]
		if left.From != right.From {
			return left.From < right.From
		}
		if left.To != right.To {
			return left.To < right.To
		}
		return left.Branch < right.Branch
	})
	schema, err := canonicalJSON(asset.InputSchema)
	if err != nil {
		return CanonicalFlow{}, fmt.Errorf("authoring: canonical input schema: %w", err)
	}
	asset.InputSchema = schema
	return asset, nil
}

// MarshalCanonicalFlow returns the byte-stable representation used by the UI,
// CLI, and CI integrations.
func MarshalCanonicalFlow(asset CanonicalFlow) ([]byte, error) {
	normalized, err := NormalizeCanonicalFlow(asset)
	if err != nil {
		return nil, err
	}
	document, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("authoring: marshal canonical flow: %w", err)
	}
	return append(document, '\n'), nil
}

func canonicalRequestHash(asset CanonicalFlow) (string, error) {
	document, err := MarshalCanonicalFlow(asset)
	if err != nil {
		return "", err
	}
	return hashText(string(bytes.TrimSpace(document))), nil
}

// rejectSensitiveFixtures prevents repository export from either leaking
// workspace-classified values or silently replacing executable source with
// redaction markers. Input schemas are deliberately excluded: a schema field
// name is a contract declaration, not an embedded subject value.
func rejectSensitiveFixtures(graph events.Graph, fields map[string]bool) error {
	if len(fields) == 0 {
		return nil
	}
	raw, err := json.Marshal(graph)
	if err != nil {
		return fmt.Errorf("authoring: inspect sensitive fixtures: %w", err)
	}
	masked := privacy.Mask(raw, fields)
	var source, redacted any
	if err := json.Unmarshal(raw, &source); err != nil {
		return fmt.Errorf("authoring: decode source for sensitive fixture check: %w", err)
	}
	if err := json.Unmarshal(masked, &redacted); err != nil {
		return fmt.Errorf("authoring: decode masked source for sensitive fixture check: %w", err)
	}
	if !reflect.DeepEqual(source, redacted) {
		return errors.New(
			"authoring: canonical export blocked because graph configuration embeds " +
				"workspace-classified sensitive values; replace fixtures with non-sensitive references",
		)
	}
	return nil
}

// canonicalComponentReferences replaces workspace-local component ids with
// repository-stable slugs before export.
func canonicalComponentReferences(
	graph events.Graph,
	slugForID map[string]string,
) (events.Graph, error) {
	out := cloneGraph(graph)
	for index, node := range out.Nodes {
		if node.Type != events.NodeSubflow {
			continue
		}
		var config SubflowConfig
		if err := json.Unmarshal(node.Config, &config); err != nil {
			return events.Graph{}, fmt.Errorf("authoring: subflow node %q config: %w", node.ID, err)
		}
		slug, ok := slugForID[config.ComponentID]
		if !ok {
			return events.Graph{}, fmt.Errorf(
				"authoring: subflow node %q references unknown component %q",
				node.ID, config.ComponentID,
			)
		}
		encoded, err := json.Marshal(SubflowConfig{
			ComponentSlug: slug,
			Version:       config.Version,
		})
		if err != nil {
			return events.Graph{}, err
		}
		out.Nodes[index].Config = encoded
	}
	return out, nil
}

// workspaceComponentReferences resolves repository-stable slugs to exact
// tenant/workspace component identities at import.
func workspaceComponentReferences(
	graph events.Graph,
	idForSlug map[string]string,
) (events.Graph, CanonicalMigrationReport, error) {
	out := cloneGraph(graph)
	report := CanonicalMigrationReport{Rewrites: []CanonicalRewrite{}}
	for index, node := range out.Nodes {
		if node.Type != events.NodeSubflow {
			continue
		}
		var config SubflowConfig
		if err := json.Unmarshal(node.Config, &config); err != nil {
			return events.Graph{}, CanonicalMigrationReport{}, fmt.Errorf(
				"authoring: canonical subflow node %q config: %w", node.ID, err,
			)
		}
		componentID, ok := idForSlug[config.ComponentSlug]
		if !ok {
			return events.Graph{}, CanonicalMigrationReport{}, fmt.Errorf(
				"authoring: canonical subflow node %q references unknown component slug %q",
				node.ID, config.ComponentSlug,
			)
		}
		encoded, err := json.Marshal(SubflowConfig{
			ComponentID: componentID,
			Version:     config.Version,
		})
		if err != nil {
			return events.Graph{}, CanonicalMigrationReport{}, err
		}
		out.Nodes[index].Config = encoded
		report.Rewrites = append(report.Rewrites, CanonicalRewrite{
			Path:   fmt.Sprintf("graph.nodes[%q].config.component_slug", node.ID),
			From:   config.ComponentSlug,
			To:     componentID,
			Reason: "resolved portable component slug to target-workspace component id",
		})
	}
	return out, report, nil
}
