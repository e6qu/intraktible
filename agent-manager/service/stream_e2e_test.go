// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/platform/testutil"
)

// defineAndWait creates a (toolless) agent and waits for the projection to show it.
func defineAndWait(t *testing.T, api *testutil.API, name string) {
	t.Helper()
	api.Request(t, http.MethodPost, "/v1/agents", map[string]any{"name": name}, http.StatusAccepted, nil)
	if !testutil.Eventually(t, func() bool {
		var a agents.AgentView
		api.Request(t, http.MethodGet, "/v1/agents/"+name, nil, http.StatusOK, &a)
		return a.Name == name
	}) {
		t.Fatalf("agent %q never appeared", name)
	}
}

func TestRunStreamSSE(t *testing.T) {
	api := start(t)
	defineAndWait(t, api, "sse")

	req, err := http.NewRequest(http.MethodGet, api.Server.URL+"/v1/agents/sse/run/stream?prompt=hello+world", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", api.Key)
	resp, err := api.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	var chunks []string
	var doneStatus, lastEvent string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if ev, ok := strings.CutPrefix(line, "event: "); ok {
			lastEvent = ev
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		switch lastEvent {
		case "chunk":
			var c struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal([]byte(data), &c)
			chunks = append(chunks, c.Text)
		case "done":
			var d struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal([]byte(data), &d)
			doneStatus = d.Status
		}
	}
	if strings.Join(chunks, "") != "stub: hello world" {
		t.Fatalf("streamed chunks = %v", chunks)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple deltas, got %d", len(chunks))
	}
	if doneStatus != "completed" {
		t.Fatalf("done status = %q", doneStatus)
	}
}

func TestRunStreamWebSocket(t *testing.T) {
	api := start(t)
	defineAndWait(t, api, "ws")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := strings.Replace(api.Server.URL, "http://", "ws://", 1) + "/v1/agents/ws/run/ws"
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Api-Key": []string{api.Key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = c.CloseNow() }()

	if err := wsjson.Write(ctx, c, map[string]string{"prompt": "hi there"}); err != nil {
		t.Fatal(err)
	}
	var texts []string
	var doneStatus string
	for {
		var m map[string]any
		if err := wsjson.Read(ctx, c, &m); err != nil {
			break
		}
		switch m["type"] {
		case "chunk":
			if s, ok := m["text"].(string); ok {
				texts = append(texts, s)
			}
		case "done":
			if s, ok := m["status"].(string); ok {
				doneStatus = s
			}
		}
		if m["type"] == "done" {
			break
		}
	}
	if strings.Join(texts, "") != "stub: hi there" {
		t.Fatalf("ws streamed = %v", texts)
	}
	if doneStatus != "completed" {
		t.Fatalf("ws done status = %q", doneStatus)
	}
}
