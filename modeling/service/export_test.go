// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/modeling/domain"
)

func TestSnapshotCSVIsDeterministicAndColumnStable(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	left := domain.DatasetRow{
		EntityID: "b", Features: map[string]float64{"z": 2, "a": 1},
		Label: []byte(`true`), LabelPresent: true,
		Segments:  map[string]string{"region": "north,west"},
		Partition: "test", ObservationAt: at, KnowledgeAt: at,
	}
	right := domain.DatasetRow{
		EntityID: "a", Features: map[string]float64{"a": 3, "z": 4},
		LabelPresent: false, Censored: true,
		Segments:  map[string]string{"region": "south"},
		Partition: "train", ObservationAt: at, KnowledgeAt: at,
	}
	first, err := snapshotCSV([]domain.DatasetRow{left, right})
	if err != nil {
		t.Fatal(err)
	}
	second, err := snapshotCSV([]domain.DatasetRow{right, left})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("CSV changed with row order:\n%s\n%s", first, second)
	}
	lines := strings.Split(strings.TrimSpace(string(first)), "\n")
	if len(lines) != 3 ||
		lines[0] != "entity_id,feature:a,feature:z,label,label_present,censored,segment:region,partition,observation_at,knowledge_at" ||
		!strings.HasPrefix(lines[1], "a,3,4,,false,true,south,train,") ||
		!strings.Contains(lines[2], `"north,west"`) {
		t.Fatalf("CSV = %s", first)
	}
}
