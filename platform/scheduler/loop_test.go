// SPDX-License-Identifier: AGPL-3.0-or-later

package scheduler

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestRunReportsFailureThenRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		wantErr := errors.New("tick failed")
		reports := make(chan error, 2)
		successes := make(chan int, 1)
		calls := 0
		go Run(
			ctx,
			time.Hour,
			"test_scheduler",
			"test",
			func(err error) { reports <- err },
			func(context.Context) (int, error) {
				calls++
				if calls == 1 {
					return 0, wantErr
				}
				return calls, nil
			},
			func(result int) { successes <- result },
		)
		synctest.Wait()

		time.Sleep(time.Hour)
		synctest.Wait()
		if got := <-reports; !errors.Is(got, wantErr) {
			t.Fatalf("first report = %v, want %v", got, wantErr)
		}
		select {
		case got := <-successes:
			t.Fatalf("failed tick reached success callback: %d", got)
		default:
		}

		time.Sleep(time.Hour)
		synctest.Wait()
		if got := <-reports; got != nil {
			t.Fatalf("recovery report = %v, want nil", got)
		}
		if got := <-successes; got != 2 {
			t.Fatalf("success callback = %d, want 2", got)
		}

		cancel()
		synctest.Wait()
	})
}
