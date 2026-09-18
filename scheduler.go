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
	candidate pluginapi.SchedulerAuthCandidate
	active    int
	queued    int
	limit     int
}

func (s *Service) Pick(request pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
	if !requestContainsCodex(request) {
		return pluginapi.SchedulerPickResponse{Handled: false}, nil
	}
	s.mu.RLock()
	enabled := s.settings.Enabled
	failurePolicy := s.settings.QueryFailurePolicy
	s.mu.RUnlock()
	if !enabled {
		return pluginapi.SchedulerPickResponse{Handled: false}, nil
	}
	ids := make(map[string]struct{})
	for _, candidate := range request.Candidates {
		if strings.EqualFold(candidate.Provider, "codex") {
			ids[candidate.ID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return pluginapi.SchedulerPickResponse{Handled: false}, nil
	}
	if err := s.RefreshAccounts(ids); err != nil && failurePolicy == failureDeny {
		return pluginapi.SchedulerPickResponse{}, fmt.Errorf("实时额度查询失败: %w", err)
	}

	now := time.Now()
	allowed := make([]schedulableCandidate, 0, len(ids))
	blockedReasons := make([]string, 0)
	s.mu.RLock()
	settings := s.settings
	for _, candidate := range request.Candidates {
		if !strings.EqualFold(candidate.Provider, "codex") {
			continue
		}
		effective := s.effectiveSettingsLocked(candidate.ID)
		account, ok := s.accounts[candidate.ID]
		if !ok {
			account = AccountSnapshot{AuthID: candidate.ID, LastError: "尚未取得账号额度"}
		}
		decision := s.accountDecision(account, effective, settings, now)
		if decision.Blocked {
			blockedReasons = append(blockedReasons, candidate.ID+": "+decision.Reason)
			continue
		}
		load := s.limiter.Snapshot(candidate.ID)
		allowed = append(allowed, schedulableCandidate{candidate: candidate, active: load.Active, queued: load.Queued, limit: effective.MaxConcurrencyPerAccount})
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
		if item.active < item.limit {
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
	for _, item := range pool {
		if item.candidate.Priority == maxPriority {
			highest = append(highest, item)
		}
	}
	minLoad := highest[0].active + highest[0].queued
	for _, item := range highest[1:] {
		if load := item.active + item.queued; load < minLoad {
			minLoad = load
		}
	}
	lightest := highest[:0]
	for _, item := range highest {
		if item.active+item.queued == minLoad {
			lightest = append(lightest, item)
		}
	}
	sort.Slice(lightest, func(i, j int) bool { return lightest[i].candidate.ID < lightest[j].candidate.ID })
	s.mu.Lock()
	key := fmt.Sprintf("%s|%s|%d", strings.ToLower(request.Provider), request.Model, maxPriority)
	index := s.rr[key] % uint64(len(lightest))
	s.rr[key]++
	selected := lightest[index].candidate
	s.mu.Unlock()
	return pluginapi.SchedulerPickResponse{AuthID: selected.ID, Handled: true}, nil
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
