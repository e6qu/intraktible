// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/ai"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestRemoteProtocolPreservesAuthorityAndReplayBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	secret := strings.Repeat("s", 32)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Idempotency-Key") != "assist-key" ||
			request.Header.Get("X-Intraktible-Agent-Protocol") != RemoteProtocolVersion {
			t.Fatalf("protocol headers = %+v", request.Header)
		}
		var invocation RemoteInvocation
		if err := json.NewDecoder(request.Body).Decode(&invocation); err != nil {
			t.Fatal(err)
		}
		capability, err := VerifyRemoteCapability(
			invocation.CapabilityToken, []byte(secret), now,
		)
		if err != nil {
			t.Fatal(err)
		}
		if capability.Org != "acme" || capability.Workspace != "risk" ||
			capability.Actor != "reviewer" || capability.TemplateID != "copilot" ||
			capability.Release != 7 || capability.SpecHash != strings.Repeat("a", 64) ||
			len(capability.EvidenceIDs) != 1 || capability.EvidenceIDs[0] != "evidence-1" {
			t.Fatalf("capability = %+v", capability)
		}
		body, err := json.Marshal(RemoteInvocationResult{
			ProtocolVersion: RemoteProtocolVersion,
			InvocationID:    invocation.InvocationID,
			IdempotencyKey:  invocation.IdempotencyKey,
			Response: ai.Response{
				Structured: json.RawMessage(`{"answer":"cited"}`),
				Model:      "remote-model",
				Usage:      ai.Usage{PromptTokens: 40, CompletionTokens: 10},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Header:     make(http.Header),
		}, nil
	})
	client := NewRemoteClient(
		&http.Client{Transport: transport},
		func(name string) (string, bool) {
			return secret, name == "REMOTE_COPILOT_SECRET"
		},
	).WithNow(func() time.Time { return now })
	release := ReleaseView{
		Org: "acme", Workspace: "risk", TemplateID: "copilot", Release: 7,
		SpecHash: strings.Repeat("a", 64),
		Spec:     remoteTestSpec(),
	}
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Minute))
	defer cancel()
	response, err := client.Complete(
		ctx, release, "reviewer", "case_assist:summary", "invocation-1", "assist-key",
		[]string{"evidence-1"}, ai.Request{Prompt: "governed input"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.Model != "remote-model" || response.Usage.Total() != 50 {
		t.Fatalf("response = %+v", response)
	}
}

func TestRemoteCapabilityRejectsTamperingAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	secret := []byte(strings.Repeat("s", 32))
	capability := RemoteCapability{
		ProtocolVersion: RemoteProtocolVersion,
		Org:             "acme", Workspace: "risk", Actor: "reviewer",
		TemplateID: "copilot", Release: 1, SpecHash: strings.Repeat("a", 64),
		InvocationID: "invocation", IdempotencyKey: "request",
		DataPurposes: []string{"case_review"}, Budget: remoteTestSpec().Budget,
		ExpiresAt: now.Add(time.Minute),
	}
	token, err := SignRemoteCapability(capability, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRemoteCapability(token+"x", secret, now); err == nil {
		t.Fatal("tampered capability was accepted")
	}
	if _, err := VerifyRemoteCapability(token, secret, capability.ExpiresAt); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("expiry error = %v", err)
	}
	if _, err := VerifyRemoteCapability(token, []byte("short"), now); err == nil ||
		!strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("short verifier secret error = %v", err)
	}
	if _, err := VerifyRemoteCapability(
		strings.Repeat("x", maxRemoteCapabilityTokenSize+1), secret, now,
	); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized capability error = %v", err)
	}
}

func TestRemoteProtocolRejectsOversizedResponse(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	secret := strings.Repeat("s", 32)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var invocation RemoteInvocation
		if err := json.NewDecoder(request.Body).Decode(&invocation); err != nil {
			t.Fatal(err)
		}
		result, err := json.Marshal(RemoteInvocationResult{
			ProtocolVersion: RemoteProtocolVersion,
			InvocationID:    invocation.InvocationID,
			IdempotencyKey:  invocation.IdempotencyKey,
			Response: ai.Response{
				Structured: json.RawMessage(`{"answer":"cited"}`),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, []byte(strings.Repeat(" ", maxRemoteResponseBytes))...)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(result))),
			Header:     make(http.Header),
		}, nil
	})
	client := NewRemoteClient(
		&http.Client{Transport: transport},
		func(string) (string, bool) { return secret, true },
	).WithNow(func() time.Time { return now })
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Minute))
	defer cancel()
	_, err := client.Complete(
		ctx,
		ReleaseView{
			Org: "acme", Workspace: "risk", TemplateID: "copilot", Release: 1,
			SpecHash: strings.Repeat("a", 64), Spec: remoteTestSpec(),
		},
		"reviewer", "evaluation", "invocation", "idempotency", nil, ai.Request{},
	)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func remoteTestSpec() ReleaseSpec {
	spec := testReleaseSpec()
	spec.Provider, spec.Model = "remote", "remote-model"
	spec.AllowRemoteAgent = true
	spec.RemoteProtocolURL = "https://agents.example.test/invoke"
	spec.RemoteProtocolVersion = RemoteProtocolVersion
	spec.RemoteCredentialEnv = "REMOTE_COPILOT_SECRET"
	return spec
}
