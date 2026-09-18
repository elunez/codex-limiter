package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type schedulableCandidate struct {
	candidate  pluginapi.SchedulerAuthCandidate
	controlled bool
	active     int
	queued     int
	limit      int
}

type sessionBinding struct {
	AuthID    string
	ExpiresAt time.Time
}

func (s *Service) Pick(request pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
	if !requestContainsCodex(request) {
		return pluginapi.SchedulerPickResponse{Handled: false}, nil
	}
	controlledIDs := make(map[string]struct{})
	s.mu.RLock()
	for _, candidate := range request.Candidates {
		if strings.EqualFold(candidate.Provider, "codex") {
			if enabled, _ := s.accountControlLocked(candidate.ID); enabled {
				controlledIDs[candidate.ID] = struct{}{}
			}
		}
	}
	s.mu.RUnlock()
	if len(controlledIDs) == 0 {
		return pluginapi.SchedulerPickResponse{Handled: false}, nil
	}
	now := time.Now()
	allowed := make([]schedulableCandidate, 0, len(request.Candidates))
	blockedReasons := make([]string, 0)
	s.mu.RLock()
	for _, candidate := range request.Candidates {
		if !strings.EqualFold(candidate.Provider, "codex") {
			continue
		}
		controlled, effective := s.accountControlLocked(candidate.ID)
		if !controlled {
			allowed = append(allowed, schedulableCandidate{candidate: candidate})
			continue
		}
		account, ok := s.accounts[candidate.ID]
		if !ok {
			account = AccountSnapshot{AuthID: candidate.ID, LastError: "尚未取得账号额度"}
		}
		decision := s.accountDecision(account, effective, now)
		if decision.Blocked {
			blockedReasons = append(blockedReasons, candidate.ID+": "+decision.Reason)
			continue
		}
		load := s.limiter.Snapshot(candidate.ID)
		allowed = append(allowed, schedulableCandidate{candidate: candidate, controlled: true, active: load.Active, queued: load.Queued, limit: effective.MaxConcurrencyPerAccount})
	}
	s.mu.RUnlock()
	if len(allowed) == 0 {
		message := "所有可选 Codex 账号均已达到额度限制或不可用"
		if len(blockedReasons) > 0 {
			message += "（" + strings.Join(blockedReasons, "；") + "）"
		}
		return pluginapi.SchedulerPickResponse{}, errors.New(message)
	}

	// 先在全部可调度账号中寻找有并发空位的账号，避免高优先级账号
	// 已满时让请求原地排队，而低优先级账号仍然空闲。只有所有账号
	// 都没有空位时，才保留完整候选集并进入后续排队选择。
	pool := allowed
	withCapacity := make([]schedulableCandidate, 0, len(allowed))
	for _, item := range allowed {
		if !item.controlled || item.active < item.limit {
			withCapacity = append(withCapacity, item)
		}
	}
	if len(withCapacity) > 0 {
		pool = withCapacity
	}

	maxPriority := pool[0].candidate.Priority
	for _, item := range pool[1:] {
		if item.candidate.Priority > maxPriority {
			maxPriority = item.candidate.Priority
		}
	}
	highest := make([]schedulableCandidate, 0, len(pool))
	byID := make(map[string]schedulableCandidate, len(allowed))
	for _, item := range allowed {
		byID[item.candidate.ID] = item
	}
	for _, item := range pool {
		if item.candidate.Priority == maxPriority {
			highest = append(highest, item)
		}
	}
	sort.Slice(highest, func(i, j int) bool { return highest[i].candidate.ID < highest[j].candidate.ID })
	s.mu.Lock()
	now = time.Now()
	s.cleanupAffinityLocked(now)
	settings := s.settings
	affinityKey := schedulerAffinityKey(request)
	keepBinding := false
	if settings.SessionAffinityEnabled && affinityKey != "" {
		if binding, ok := s.affinity[affinityKey]; ok {
			if !binding.ExpiresAt.After(now) {
				delete(s.affinity, affinityKey)
			} else if item, exists := byID[binding.AuthID]; exists && (!item.controlled || item.active < item.limit) {
				binding.ExpiresAt = now.Add(time.Duration(settings.SessionAffinityTTLSeconds) * time.Second)
				s.affinity[affinityKey] = binding
				selected := item.candidate
				s.mu.Unlock()
				return pluginapi.SchedulerPickResponse{AuthID: selected.ID, Handled: true}, nil
			} else if _, exists := byID[binding.AuthID]; exists {
				// A full bound account is allowed to overflow temporarily. Keep the
				// binding so the next request returns to it after capacity recovers.
				keepBinding = true
			} else {
				delete(s.affinity, affinityKey)
			}
		}
	}
	key := fmt.Sprintf("%s|%s|%d", strings.ToLower(request.Provider), request.Model, maxPriority)
	index := s.rr[key] % uint64(len(highest))
	s.rr[key]++
	selected := highest[index].candidate
	if settings.SessionAffinityEnabled && affinityKey != "" && !keepBinding {
		s.affinity[affinityKey] = sessionBinding{AuthID: selected.ID, ExpiresAt: now.Add(time.Duration(settings.SessionAffinityTTLSeconds) * time.Second)}
	}
	s.mu.Unlock()
	return pluginapi.SchedulerPickResponse{AuthID: selected.ID, Handled: true}, nil
}

func (s *Service) cleanupAffinityLocked(now time.Time) {
	if now.Sub(s.affinityGC) < time.Minute {
		return
	}
	for key, binding := range s.affinity {
		if !binding.ExpiresAt.After(now) {
			delete(s.affinity, key)
		}
	}
	s.affinityGC = now
}

func schedulerAffinityKey(request pluginapi.SchedulerPickRequest) string {
	sessionID := schedulerSessionID(request.Options)
	if sessionID == "" {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(request.Provider))
	if provider == "" {
		for _, candidate := range request.Candidates {
			if strings.EqualFold(candidate.Provider, "codex") {
				provider = "codex"
				break
			}
		}
	}
	return strings.Join([]string{provider, strings.TrimSpace(request.Model), sessionID}, "|")
}

func schedulerSessionID(options pluginapi.SchedulerOptions) string {
	for _, key := range []string{"canonical_session_id", "execution_session_id", "derived_session_id", "session_id", "sessionId"} {
		if value, ok := options.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	for _, key := range []string{"X-Claude-Code-Session-Id", "Session-Id", "Session_id", "X-Session-ID", "X-Session-Affinity", "X-Client-Request-Id"} {
		for header, values := range options.Headers {
			if strings.EqualFold(header, key) && len(values) > 0 && strings.TrimSpace(values[0]) != "" {
				return strings.TrimSpace(values[0])
			}
		}
	}
	return ""
}

func requestContainsCodex(request pluginapi.SchedulerPickRequest) bool {
	if strings.EqualFold(request.Provider, "codex") {
		return true
	}
	for _, provider := range request.Providers {
		if strings.EqualFold(provider, "codex") {
			return true
		}
	}
	for _, candidate := range request.Candidates {
		if strings.EqualFold(candidate.Provider, "codex") {
			return true
		}
	}
	return false
}
