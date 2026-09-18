package main

import (
	"errors"
	"sync"
	"time"
)

var (
	errQueueTimeout = errors.New("account concurrency queue timeout")
	errStopped      = errors.New("concurrency limiter stopped")
)

type limiterSnapshot struct {
	Active int `json:"active"`
	Queued int `json:"queued"`
}

// accountLimiter 按宿主 auth ID 统计占位。每次占位传入当前账号的有效上限，
// 因此全局设置和账号覆盖可以热更新，已有请求不会被强制中断。
type accountLimiter struct {
	mu           sync.Mutex
	active       map[string]int
	reservations map[string]string
	waiting      map[string]string
	notify       chan struct{}
	stopped      bool
}

func newAccountLimiter(_ int) *accountLimiter {
	return &accountLimiter{
		active:       make(map[string]int),
		reservations: make(map[string]string),
		waiting:      make(map[string]string),
		notify:       make(chan struct{}),
	}
}

func (l *accountLimiter) Acquire(requestID, authID string, limit int, timeout time.Duration) error {
	if limit < 1 {
		limit = 1
	}
	deadline := time.Now().Add(timeout)
	for {
		l.mu.Lock()
		if l.stopped {
			delete(l.waiting, requestID)
			l.mu.Unlock()
			return errStopped
		}

		if currentAuth, exists := l.reservations[requestID]; exists {
			if currentAuth == authID {
				delete(l.waiting, requestID)
				l.mu.Unlock()
				return nil
			}
			l.releaseLocked(requestID, currentAuth)
		}

		if l.active[authID] < limit {
			delete(l.waiting, requestID)
			l.active[authID]++
			l.reservations[requestID] = authID
			l.mu.Unlock()
			return nil
		}

		if timeout == 0 {
			delete(l.waiting, requestID)
			l.mu.Unlock()
			return errQueueTimeout
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			delete(l.waiting, requestID)
			l.mu.Unlock()
			return errQueueTimeout
		}
		l.waiting[requestID] = authID
		notify := l.notify
		l.mu.Unlock()

		timer := time.NewTimer(remaining)
		select {
		case <-notify:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			l.mu.Lock()
			delete(l.waiting, requestID)
			l.mu.Unlock()
			return errQueueTimeout
		}
	}
}

func (l *accountLimiter) Release(requestID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.waiting, requestID)
	authID, exists := l.reservations[requestID]
	if !exists {
		return
	}
	l.releaseLocked(requestID, authID)
}

func (l *accountLimiter) releaseLocked(requestID, authID string) {
	delete(l.reservations, requestID)
	if count := l.active[authID]; count <= 1 {
		delete(l.active, authID)
	} else {
		l.active[authID] = count - 1
	}
	l.signalLocked()
}

func (l *accountLimiter) Wake() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.signalLocked()
}

func (l *accountLimiter) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	l.stopped = true
	l.signalLocked()
}

func (l *accountLimiter) signalLocked() {
	close(l.notify)
	l.notify = make(chan struct{})
}

func (l *accountLimiter) Snapshot(authID string) limiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	queued := 0
	for _, waitingAuthID := range l.waiting {
		if waitingAuthID == authID {
			queued++
		}
	}
	return limiterSnapshot{Active: l.active[authID], Queued: queued}
}

func (l *accountLimiter) activeCount(authID string) int {
	return l.Snapshot(authID).Active
}
