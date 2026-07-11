// SPDX-License-Identifier: AGPL-3.0-or-later

package analytics

import (
	"testing"
	"time"
)

func TestBumpDayAccumulatesSortsAndPrunes(t *testing.T) {
	var m FlowMetrics
	// The same day accumulates in place rather than appending a duplicate.
	bumpDay(&m, "2026-07-11", func(b *DailyBucket) { b.Completed++; b.TotalDurationMS += 10 })
	bumpDay(&m, "2026-07-11", func(b *DailyBucket) { b.Failed++ })
	if len(m.Daily) != 1 || m.Daily[0].Completed != 1 || m.Daily[0].Failed != 1 {
		t.Fatalf("same day should accumulate: %+v", m.Daily)
	}
	// An out-of-order older day still lands in sorted position (replay determinism).
	bumpDay(&m, "2026-07-05", func(b *DailyBucket) { b.Completed++ })
	if m.Daily[0].Date != "2026-07-05" {
		t.Fatalf("buckets must stay date-sorted: %+v", m.Daily)
	}
	// Past the retention bound only the most recent maxDailyBuckets survive, so the
	// projection stays bounded no matter how long the flow lives.
	base := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for d := 1; d <= maxDailyBuckets+30; d++ {
		bumpDay(&m, dayKey(base.AddDate(0, 0, d)), func(b *DailyBucket) { b.Completed++ })
	}
	if len(m.Daily) != maxDailyBuckets {
		t.Fatalf("retention should cap at %d, got %d", maxDailyBuckets, len(m.Daily))
	}
	for i := 1; i < len(m.Daily); i++ {
		if m.Daily[i-1].Date >= m.Daily[i].Date {
			t.Fatalf("survivors must be sorted ascending, broke at %d: %+v", i, m.Daily)
		}
	}
}
