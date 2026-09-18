package main

import (
	"errors"
	"testing"
	"time"
)

func TestAccountLimiterQueuesUntilSlotIsReleased(t *testing.T) {
	limiter := newAccountLimiter(2)
	if err := limiter.Acquire("request-1", "auth-a", 2, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Acquire("request-2", "auth-a", 2, time.Second); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- limiter.Acquire("request-3", "auth-a", 2, time.Second) }()
	time.Sleep(30 * time.Millisecond)
	if got := limiter.Snapshot("auth-a"); got.Active != 2 || got.Queued != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	limiter.Release("request-1")
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not acquire slot")
	}
	if got := limiter.Snapshot("auth-a"); got.Active != 2 || got.Queued != 0 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestAccountLimiterTimesOutAndCleansQueue(t *testing.T) {
	limiter := newAccountLimiter(1)
	if err := limiter.Acquire("request-1", "auth-a", 1, time.Second); err != nil {
		t.Fatal(err)
	}
	err := limiter.Acquire("request-2", "auth-a", 1, 25*time.Millisecond)
	if !errors.Is(err, errQueueTimeout) {
		t.Fatalf("error = %v", err)
	}
	if got := limiter.Snapshot("auth-a").Queued; got != 0 {
		t.Fatalf("queued = %d", got)
	}
}

func TestAccountLimiterDoesNotDoubleCountRetryAndCanMoveAccount(t *testing.T) {
	limiter := newAccountLimiter(1)
	if err := limiter.Acquire("request-1", "auth-a", 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Acquire("request-1", "auth-a", 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Acquire("request-1", "auth-b", 1, 0); err != nil {
		t.Fatal(err)
	}
	if limiter.activeCount("auth-a") != 0 || limiter.activeCount("auth-b") != 1 {
		t.Fatal("reservation was not moved")
	}
}
