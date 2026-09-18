package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestSchedulerSkipsQuotaBlockedAccount(t *testing.T) {
	host := &fakeHostClient{
		entries:  []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex", Priority: 8}, {ID: "auth-b", AuthIndex: "index-b", Provider: "codex", Priority: 7}, {ID: "auth-c", AuthIndex: "index-c", Provider: "codex", Priority: 6}},
		authJSON: json.RawMessage(`{"access_token":"token"}`),
	}
	host.httpResult = pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":95,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	service.overrides["auth-a"] = AccountOverride{Settings: AccountSettings{MaxConcurrencyPerAccount: 3, QueueTimeoutSeconds: 300, FiveHour: WindowRule{Enabled: true, CutoffPercent: 95}, Weekly: WindowRule{Enabled: true, CutoffPercent: 95}, MatchPolicy: matchAny}}
	if err := service.RefreshAccounts(nil); err != nil {
		t.Fatal(err)
	}
	response, err := service.Pick(pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-5", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex", Priority: 8}, {ID: "auth-b", Provider: "codex", Priority: 7}, {ID: "auth-c", Provider: "codex", Priority: 6}}})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Handled || response.AuthID != "auth-b" {
		t.Fatalf("response = %+v", response)
	}
}

func TestSchedulerPrefersAccountWithFreeCapacity(t *testing.T) {
	host := &fakeHostClient{entries: []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex", Priority: 10}, {ID: "auth-b", AuthIndex: "index-b", Provider: "codex", Priority: 10}}, authJSON: json.RawMessage(`{"access_token":"token"}`), httpResult: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)}}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.limiter.Acquire("r1", "auth-a", 3, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.limiter.Acquire("r2", "auth-a", 3, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.limiter.Acquire("r3", "auth-a", 3, 0); err != nil {
		t.Fatal(err)
	}
	service.overrides["auth-a"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	service.overrides["auth-b"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	response, err := service.Pick(pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex", Priority: 10}, {ID: "auth-b", Provider: "codex", Priority: 10}}})
	if err != nil || response.AuthID != "auth-b" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestSchedulerUsesLowerPriorityAccountWhenHigherPriorityIsFull(t *testing.T) {
	host := &fakeHostClient{entries: []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex", Priority: 8}, {ID: "auth-b", AuthIndex: "index-b", Provider: "codex", Priority: 7}, {ID: "auth-c", AuthIndex: "index-c", Provider: "codex", Priority: 6}}, authJSON: json.RawMessage(`{"access_token":"token"}`), httpResult: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)}}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.limiter.Acquire("r1", "auth-a", 3, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.limiter.Acquire("r2", "auth-a", 3, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.limiter.Acquire("r3", "auth-a", 3, 0); err != nil {
		t.Fatal(err)
	}
	service.overrides["auth-a"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	service.overrides["auth-b"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	service.overrides["auth-c"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	response, err := service.Pick(pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex", Priority: 8}, {ID: "auth-b", Provider: "codex", Priority: 7}, {ID: "auth-c", Provider: "codex", Priority: 6}}})
	if err != nil || response.AuthID != "auth-b" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestSchedulerDefersToHostWhenNoAccountHasControlEnabled(t *testing.T) {
	service := testService(true, 3, 0)
	response, err := service.Pick(pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex"}}})
	if err != nil || response.Handled {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestSchedulerUsesUncontrolledAccountWhenControlledAccountIsBlocked(t *testing.T) {
	host := &fakeHostClient{
		entries:    []pluginapi.HostAuthFileEntry{{ID: "auth-controlled", AuthIndex: "index-controlled", Provider: "codex", Priority: 10}, {ID: "auth-open", AuthIndex: "index-open", Provider: "codex", Priority: 10}},
		authJSON:   json.RawMessage(`{"access_token":"token"}`),
		httpResult: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)},
	}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	service.overrides["auth-controlled"] = AccountOverride{Settings: AccountSettings{MaxConcurrencyPerAccount: 3, QueueTimeoutSeconds: 0, FiveHour: WindowRule{Enabled: true, CutoffPercent: 10}, Weekly: WindowRule{Enabled: false}, MatchPolicy: matchAny}}
	if err := service.RefreshAccounts(nil); err != nil {
		t.Fatal(err)
	}
	response, err := service.Pick(pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-controlled", Provider: "codex", Priority: 10}, {ID: "auth-open", Provider: "codex", Priority: 10}}})
	if err != nil || !response.Handled || response.AuthID != "auth-open" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestSchedulerBindsSessionAndKeepsRoundRobinForNewSessions(t *testing.T) {
	host := &fakeHostClient{
		entries:    []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex", Priority: 10}, {ID: "auth-b", AuthIndex: "index-b", Provider: "codex", Priority: 10}},
		authJSON:   json.RawMessage(`{"access_token":"token"}`),
		httpResult: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)},
	}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	service.overrides["auth-a"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	service.overrides["auth-b"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	request := func(session string) pluginapi.SchedulerPickRequest {
		return pluginapi.SchedulerPickRequest{
			Provider: "codex",
			Model:    "gpt-5",
			Options:  pluginapi.SchedulerOptions{Metadata: map[string]any{"canonical_session_id": session}},
			Candidates: []pluginapi.SchedulerAuthCandidate{
				{ID: "auth-a", Provider: "codex", Priority: 10},
				{ID: "auth-b", Provider: "codex", Priority: 10},
			},
		}
	}
	first, err := service.Pick(request("session-a"))
	if err != nil || first.AuthID != "auth-a" {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	sticky, err := service.Pick(request("session-a"))
	if err != nil || sticky.AuthID != "auth-a" {
		t.Fatalf("sticky = %+v err=%v", sticky, err)
	}
	second, err := service.Pick(request("session-b"))
	if err != nil || second.AuthID != "auth-b" {
		t.Fatalf("second session = %+v err=%v", second, err)
	}
}

func TestSchedulerUsesLowerPriorityOnlyAfterHigherPriorityIsFull(t *testing.T) {
	host := &fakeHostClient{
		entries:    []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex", Priority: 10}, {ID: "auth-b", AuthIndex: "index-b", Provider: "codex", Priority: 10}, {ID: "auth-c", AuthIndex: "index-c", Provider: "codex", Priority: 9}},
		authJSON:   json.RawMessage(`{"access_token":"token"}`),
		httpResult: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)},
	}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	for _, authID := range []string{"auth-a", "auth-b", "auth-c"} {
		service.overrides[authID] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	}
	request := func() pluginapi.SchedulerPickRequest {
		return pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-5", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex", Priority: 10}, {ID: "auth-b", Provider: "codex", Priority: 10}, {ID: "auth-c", Provider: "codex", Priority: 9}}}
	}
	for index, want := range []string{"auth-a", "auth-b"} {
		response, err := service.Pick(request())
		if err != nil || response.AuthID != want {
			t.Fatalf("round robin %d = %+v err=%v", index, response, err)
		}
	}
	for index, authID := range []string{"auth-a", "auth-b"} {
		for requestID := 0; requestID < 3; requestID++ {
			if err := service.limiter.Acquire(authID+string(rune('0'+requestID)), authID, 3, 0); err != nil {
				t.Fatalf("fill %s/%d: %v", authID, requestID, err)
			}
		}
		_ = index
	}
	response, err := service.Pick(request())
	if err != nil || response.AuthID != "auth-c" {
		t.Fatalf("lower priority = %+v err=%v", response, err)
	}
}

func TestSchedulerPreservesSessionBindingWhenBoundAccountIsTemporarilyFull(t *testing.T) {
	host := &fakeHostClient{
		entries:    []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex", Priority: 10}, {ID: "auth-b", AuthIndex: "index-b", Provider: "codex", Priority: 10}},
		authJSON:   json.RawMessage(`{"access_token":"token"}`),
		httpResult: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)},
	}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	service.overrides["auth-a"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	service.overrides["auth-b"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	request := pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-5", Options: pluginapi.SchedulerOptions{Metadata: map[string]any{"canonical_session_id": "session-a"}}, Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex", Priority: 10}, {ID: "auth-b", Provider: "codex", Priority: 10}}}
	first, err := service.Pick(request)
	if err != nil || first.AuthID != "auth-a" {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	for requestID := 0; requestID < 3; requestID++ {
		if err := service.limiter.Acquire("full-"+string(rune('0'+requestID)), "auth-a", 3, 0); err != nil {
			t.Fatal(err)
		}
	}
	overflow, err := service.Pick(request)
	if err != nil || overflow.AuthID != "auth-b" {
		t.Fatalf("overflow = %+v err=%v", overflow, err)
	}
	service.limiter.Release("full-0")
	service.limiter.Release("full-1")
	service.limiter.Release("full-2")
	recovered, err := service.Pick(request)
	if err != nil || recovered.AuthID != "auth-a" {
		t.Fatalf("recovered = %+v err=%v", recovered, err)
	}
}

func TestSchedulerRebindsSessionWhenBoundAccountIsNoLongerEligible(t *testing.T) {
	host := &fakeHostClient{
		entries:    []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex", Priority: 10}, {ID: "auth-b", AuthIndex: "index-b", Provider: "codex", Priority: 10}},
		authJSON:   json.RawMessage(`{"access_token":"token"}`),
		httpResult: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)},
	}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	service.overrides["auth-a"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	service.overrides["auth-b"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	request := pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-5", Options: pluginapi.SchedulerOptions{Metadata: map[string]any{"canonical_session_id": "session-a"}}, Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex", Priority: 10}, {ID: "auth-b", Provider: "codex", Priority: 10}}}
	first, err := service.Pick(request)
	if err != nil || first.AuthID != "auth-a" {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	host.entries = host.entries[1:]
	request.Candidates = request.Candidates[1:]
	rebound, err := service.Pick(request)
	if err != nil || rebound.AuthID != "auth-b" {
		t.Fatalf("rebound = %+v err=%v", rebound, err)
	}
	service.mu.Lock()
	binding := service.affinity[schedulerAffinityKey(request)]
	service.mu.Unlock()
	if binding.AuthID != "auth-b" {
		t.Fatalf("binding = %+v", binding)
	}
}

func TestSchedulerIgnoresExpiredSessionBinding(t *testing.T) {
	host := &fakeHostClient{
		entries:    []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex", Priority: 10}, {ID: "auth-b", AuthIndex: "index-b", Provider: "codex", Priority: 10}},
		authJSON:   json.RawMessage(`{"access_token":"token"}`),
		httpResult: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)},
	}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	service.settings.SessionAffinityEnabled = true
	service.settings.SessionAffinityTTLSeconds = 60
	service.rr = map[string]uint64{"codex|gpt-5|10": 1}
	request := pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-5", Options: pluginapi.SchedulerOptions{Metadata: map[string]any{"canonical_session_id": "session-a"}}, Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex", Priority: 10}, {ID: "auth-b", Provider: "codex", Priority: 10}}}
	key := schedulerAffinityKey(request)
	service.affinity[key] = sessionBinding{AuthID: "auth-a", ExpiresAt: time.Now().Add(-time.Second)}
	service.overrides["auth-a"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	service.overrides["auth-b"] = AccountOverride{Settings: accountSettingsFromGlobal(service.settings)}
	response, err := service.Pick(request)
	if err != nil || response.AuthID != "auth-b" {
		t.Fatalf("expired binding response = %+v err=%v", response, err)
	}
}
