// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// Memory is an in-process Store. Projections are rebuilt from the event log at
// boot, so an ephemeral store is sufficient for the MVP; durable SQLite/Postgres
// adapters are added behind this same interface for large projections.
type Memory struct {
	mu          sync.RWMutex
	collections map[string]map[string]json.RawMessage
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{collections: make(map[string]map[string]json.RawMessage)}
}

func (m *Memory) Put(_ context.Context, collection, key string, doc json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.collections[collection]
	if !ok {
		c = make(map[string]json.RawMessage)
		m.collections[collection] = c
	}
	cp := make(json.RawMessage, len(doc))
	copy(cp, doc)
	c[key] = cp
	return nil
}

func (m *Memory) Get(_ context.Context, collection, key string) (json.RawMessage, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	doc, ok := m.collections[collection][key]
	if !ok {
		return nil, false, nil
	}
	cp := make(json.RawMessage, len(doc))
	copy(cp, doc)
	return cp, true, nil
}

func (m *Memory) List(_ context.Context, collection, keyPrefix string) ([]Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c := m.collections[collection]
	out := make([]Record, 0, len(c))
	for k, v := range c {
		if keyPrefix != "" && !strings.HasPrefix(k, keyPrefix) {
			continue
		}
		cp := make(json.RawMessage, len(v))
		copy(cp, v)
		out = append(out, Record{Key: k, Doc: cp})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *Memory) Delete(_ context.Context, collection, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.collections[collection], key)
	return nil
}

func (m *Memory) Reset(_ context.Context, collection string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.collections, collection)
	return nil
}

func (m *Memory) Close() error { return nil }

// Ephemeral marks Memory as a non-durable store: it holds no transaction support
// because a restart loses everything and the projection runtime fully rebuilds
// from the log. (Marker for store.Ephemeral — see the projection runtime.)
func (m *Memory) Ephemeral() {}

// Collections returns the names of the non-empty collections, sorted. It backs
// operator tooling that reports rebuilt projection state.
func (m *Memory) Collections() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.collections))
	for name, docs := range m.collections {
		if len(docs) > 0 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
