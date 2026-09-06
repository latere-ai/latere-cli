// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestFrameStreamDelaysAfterDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var attempts []time.Time
		fs := newFrameStream(t.Context(), func(ctx context.Context, since int64) (*attachConn, error) {
			mu.Lock()
			attempts = append(attempts, time.Now())
			frames := make(chan attachFrame)
			if len(attempts) < 3 {
				close(frames)
			}
			mu.Unlock()
			return &attachConn{frames: frames, cancel: func() {}}, nil
		})
		defer fs.Close()
		for want := 1; want <= 3; want++ {
			synctest.Wait()
			mu.Lock()
			if len(attempts) != want {
				t.Errorf("dial count = %d, want %d before next reconnect delay", len(attempts), want)
			}
			mu.Unlock()
			if want < 3 {
				time.Sleep(reconnectBackoff)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		for i := 1; i < len(attempts); i++ {
			if delay := attempts[i].Sub(attempts[i-1]); delay < reconnectBackoff {
				t.Errorf("reconnect delay = %v, want at least %v", delay, reconnectBackoff)
			}
		}
	})
}

func TestFrameStreamCancelDuringDisconnectBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		fs := newFrameStream(t.Context(), func(ctx context.Context, since int64) (*attachConn, error) {
			attempts++
			frames := make(chan attachFrame)
			if attempts == 1 {
				close(frames)
			}
			return &attachConn{frames: frames, cancel: func() {}}, nil
		})
		defer fs.Close()
		synctest.Wait()
		started := time.Now()
		fs.Close()
		for range fs.Events() {
		}
		if attempts != 1 || time.Since(started) != 0 || fs.Err() != nil {
			t.Fatalf("cancel during backoff: attempts=%d elapsed=%v error=%v", attempts, time.Since(started), fs.Err())
		}
	})
}
