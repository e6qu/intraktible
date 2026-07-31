// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/ai"
)

// RemoteProtocolVersion is the wire contract separately hosted agents must
// implement. Releases pin it so a protocol change is a governed release change.
const RemoteProtocolVersion = "2026-07-01"

const (
	maxRemoteResponseBytes       = 2 << 20
	maxRemoteCapabilityTokenSize = 2 << 20
)

// SecretResolver resolves a reviewed environment-variable name to its secret.
// The secret itself is never part of the release or event log.
type SecretResolver func(string) (string, bool)

// RemoteCapability is the signed, short-lived authority a remote implementation
// receives. ToolRequests are requests it may return for platform adjudication;
// they are not proof that a tool ran and never grant remote-side execution.
type RemoteCapability struct {
	ProtocolVersion string       `json:"protocol_version"`
	Org             string       `json:"org"`
	Workspace       string       `json:"workspace"`
	Actor           string       `json:"actor"`
	TemplateID      string       `json:"template_id"`
	Release         int          `json:"release"`
	SpecHash        string       `json:"spec_hash"`
	InvocationID    string       `json:"invocation_id"`
	IdempotencyKey  string       `json:"idempotency_key"`
	DataPurposes    []string     `json:"data_purposes"`
	EvidenceIDs     []string     `json:"evidence_ids,omitempty"`
	ToolRequests    []ToolPolicy `json:"tool_requests,omitempty"`
	Budget          Budget       `json:"budget"`
	ExpiresAt       time.Time    `json:"expires_at"`
}

// RemoteInvocation is the strict request envelope served at a release's
// RemoteProtocolURL.
type RemoteInvocation struct {
	ProtocolVersion string      `json:"protocol_version"`
	InvocationID    string      `json:"invocation_id"`
	IdempotencyKey  string      `json:"idempotency_key"`
	CorrelationID   string      `json:"correlation_id"`
	Task            string      `json:"task"`
	CapabilityToken string      `json:"capability_token"`
	Request         ai.Request  `json:"request"`
	Deadline        time.Time   `json:"deadline"`
	Trace           RemoteTrace `json:"trace"`
}

// RemoteTrace propagates platform identity without sending API keys.
type RemoteTrace struct {
	Org        string `json:"org"`
	Workspace  string `json:"workspace"`
	Actor      string `json:"actor"`
	TemplateID string `json:"template_id"`
	Release    int    `json:"release"`
	SpecHash   string `json:"spec_hash"`
}

// RemoteInvocationResult must echo the replay boundaries. A model reply without
// these exact echoes is rejected before any terminal event is recorded.
type RemoteInvocationResult struct {
	ProtocolVersion string      `json:"protocol_version"`
	InvocationID    string      `json:"invocation_id"`
	IdempotencyKey  string      `json:"idempotency_key"`
	Response        ai.Response `json:"response"`
}

type RemoteClient struct {
	httpClient *http.Client
	secrets    SecretResolver
	now        func() time.Time
}

func NewRemoteClient(httpClient *http.Client, secrets SecretResolver) *RemoteClient {
	return &RemoteClient{
		httpClient: httpClient, secrets: secrets,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (c *RemoteClient) WithNow(now func() time.Time) *RemoteClient {
	c.now = now
	return c
}

func (c *RemoteClient) Complete(
	ctx context.Context,
	release ReleaseView,
	actor, task, invocationID, idempotencyKey string,
	evidenceIDs []string,
	request ai.Request,
) (ai.Response, error) {
	if c == nil || c.httpClient == nil || c.secrets == nil {
		return ai.Response{}, errors.New(
			"agent governance: remote agent client is not configured",
		)
	}
	if !release.Spec.AllowRemoteAgent {
		return ai.Response{}, errors.New(
			"agent governance: release does not permit remote execution",
		)
	}
	secret, found := c.secrets(release.Spec.RemoteCredentialEnv)
	if !found || len(secret) < 32 {
		return ai.Response{}, fmt.Errorf(
			"agent governance: remote credential %q is missing or shorter than 32 bytes",
			release.Spec.RemoteCredentialEnv,
		)
	}
	deadline, found := ctx.Deadline()
	if !found {
		return ai.Response{}, errors.New(
			"agent governance: remote invocation requires an explicit deadline",
		)
	}
	if !c.now().Before(deadline) {
		return ai.Response{}, errors.New("agent governance: remote invocation deadline has elapsed")
	}
	capability := RemoteCapability{
		ProtocolVersion: RemoteProtocolVersion,
		Org:             release.Org, Workspace: release.Workspace, Actor: actor,
		TemplateID: release.TemplateID, Release: release.Release, SpecHash: release.SpecHash,
		InvocationID: invocationID, IdempotencyKey: idempotencyKey,
		DataPurposes: append([]string(nil), release.Spec.DataPurposes...),
		EvidenceIDs:  append([]string(nil), evidenceIDs...),
		ToolRequests: append([]ToolPolicy(nil), release.Spec.Tools...),
		Budget:       release.Spec.Budget, ExpiresAt: deadline.UTC(),
	}
	token, err := SignRemoteCapability(capability, []byte(secret))
	if err != nil {
		return ai.Response{}, err
	}
	payload := RemoteInvocation{
		ProtocolVersion: RemoteProtocolVersion, InvocationID: invocationID,
		IdempotencyKey: idempotencyKey, CorrelationID: invocationID, Task: task,
		CapabilityToken: token, Request: request, Deadline: deadline.UTC(),
		Trace: RemoteTrace{
			Org: release.Org, Workspace: release.Workspace, Actor: actor,
			TemplateID: release.TemplateID, Release: release.Release, SpecHash: release.SpecHash,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ai.Response{}, fmt.Errorf("agent governance: encode remote invocation: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, release.Spec.RemoteProtocolURL, bytes.NewReader(body),
	)
	if err != nil {
		return ai.Response{}, fmt.Errorf("agent governance: build remote invocation: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Idempotency-Key", idempotencyKey)
	httpRequest.Header.Set("X-Intraktible-Agent-Protocol", RemoteProtocolVersion)
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return ai.Response{}, fmt.Errorf("agent governance: remote invocation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		if readErr != nil {
			return ai.Response{}, fmt.Errorf(
				"agent governance: remote status %d and unreadable error body: %w",
				response.StatusCode, readErr,
			)
		}
		return ai.Response{}, fmt.Errorf(
			"agent governance: remote status %d: %s",
			response.StatusCode, strings.TrimSpace(string(detail)),
		)
	}
	body, err = io.ReadAll(io.LimitReader(response.Body, maxRemoteResponseBytes+1))
	if err != nil {
		return ai.Response{}, fmt.Errorf("agent governance: read remote result: %w", err)
	}
	if len(body) > maxRemoteResponseBytes {
		return ai.Response{}, fmt.Errorf(
			"agent governance: remote result exceeds %d bytes", maxRemoteResponseBytes,
		)
	}
	var result RemoteInvocationResult
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return ai.Response{}, fmt.Errorf("agent governance: decode remote result: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return ai.Response{}, err
	}
	if result.ProtocolVersion != RemoteProtocolVersion ||
		result.InvocationID != invocationID ||
		result.IdempotencyKey != idempotencyKey {
		return ai.Response{}, errors.New(
			"agent governance: remote result changed protocol, invocation, or idempotency lineage",
		)
	}
	if result.Response.Usage.PromptTokens < 0 || result.Response.Usage.CompletionTokens < 0 {
		return ai.Response{}, errors.New(
			"agent governance: remote result contains negative token usage",
		)
	}
	return result.Response, nil
}

// SignRemoteCapability produces a deterministic HMAC-SHA256 token suitable for
// validation by a separately hosted implementation.
func SignRemoteCapability(capability RemoteCapability, secret []byte) (string, error) {
	if len(secret) < 32 {
		return "", errors.New("agent governance: remote capability secret must be at least 32 bytes")
	}
	payload, err := json.Marshal(capability)
	if err != nil {
		return "", fmt.Errorf("agent governance: encode remote capability: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature, nil
}

// VerifyRemoteCapability is the reference verifier for the versioned protocol.
func VerifyRemoteCapability(
	token string,
	secret []byte,
	now time.Time,
) (RemoteCapability, error) {
	if len(secret) < 32 {
		return RemoteCapability{}, errors.New(
			"agent governance: remote capability secret must be at least 32 bytes",
		)
	}
	if len(token) > maxRemoteCapabilityTokenSize {
		return RemoteCapability{}, fmt.Errorf(
			"agent governance: remote capability exceeds %d bytes",
			maxRemoteCapabilityTokenSize,
		)
	}
	encoded, signature, found := strings.Cut(token, ".")
	if !found || encoded == "" || signature == "" {
		return RemoteCapability{}, errors.New("agent governance: malformed remote capability")
	}
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return RemoteCapability{}, errors.New("agent governance: malformed capability signature")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(encoded))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return RemoteCapability{}, errors.New("agent governance: invalid capability signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return RemoteCapability{}, errors.New("agent governance: malformed capability payload")
	}
	var capability RemoteCapability
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capability); err != nil {
		return RemoteCapability{}, fmt.Errorf("agent governance: decode remote capability: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return RemoteCapability{}, err
	}
	if capability.ProtocolVersion != RemoteProtocolVersion ||
		capability.Org == "" || capability.Workspace == "" || capability.Actor == "" ||
		capability.TemplateID == "" || capability.Release < 1 ||
		capability.SpecHash == "" || capability.InvocationID == "" ||
		capability.IdempotencyKey == "" {
		return RemoteCapability{}, errors.New(
			"agent governance: remote capability lineage is incomplete",
		)
	}
	if !now.UTC().Before(capability.ExpiresAt) {
		return RemoteCapability{}, errors.New("agent governance: remote capability expired")
	}
	if err := capability.Budget.Validate(); err != nil {
		return RemoteCapability{}, err
	}
	return capability, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("agent governance: remote response contains trailing JSON")
		}
		return fmt.Errorf("agent governance: decode trailing remote response: %w", err)
	}
	return nil
}
