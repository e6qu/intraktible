// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"cmp"
	"context"
	"sort"
	"time"
)

// SortByTime stably orders xs by the time key t, breaking ties by the secondary key
// tie. newestFirst picks the direction (and the tiebreak: higher tie first for
// newest, lower first for oldest). Read models list documents by a creation time
// that can collide within a clock tick, so the tiebreaker (a monotonic seq / id)
// gives a total, reproducible order across reads and across an incremental vs
// rebuilt-from-zero projection.
func SortByTime[T any, S cmp.Ordered](xs []T, t func(T) time.Time, tie func(T) S, newestFirst bool) {
	sort.SliceStable(xs, func(i, j int) bool {
		ti, tj := t(xs[i]), t(xs[j])
		if !ti.Equal(tj) {
			if newestFirst {
				return ti.After(tj)
			}
			return ti.Before(tj)
		}
		if newestFirst {
			return tie(xs[i]) > tie(xs[j])
		}
		return tie(xs[i]) < tie(xs[j])
	})
}

// ListByTime lists a collection's docs under prefix and orders them by SortByTime —
// the common read-model "list all for the tenant, time-ordered" path in one call.
func ListByTime[T any, S cmp.Ordered](ctx context.Context, s Store, collection, prefix string, t func(T) time.Time, tie func(T) S, newestFirst bool) ([]T, error) {
	out, err := ListDocs[T](ctx, s, collection, prefix)
	if err != nil {
		return nil, err
	}
	SortByTime(out, t, tie, newestFirst)
	return out, nil
}
