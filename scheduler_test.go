package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

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
	if err := service.limiter.Acquire("r1", "auth-a", 2, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.limiter.Acquire("r2", "auth-a", 2, 0); err != nil {
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
	response, err := service.Pick(pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-controlled", Provider: "codex", Priority: 10}, {ID: "auth-open", Provider: "codex", Priority: 10}}})
	if err != nil || !response.Handled || response.AuthID != "auth-open" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
