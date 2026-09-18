package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestSchedulerSkipsQuotaBlockedAccount(t *testing.T) {
	host := &fakeHostClient{
		entries:  []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex", Priority: 10}, {ID: "auth-b", AuthIndex: "index-b", Provider: "codex", Priority: 10}},
		authJSON: json.RawMessage(`{"access_token":"token"}`),
	}
	host.httpResult = pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	service.accounts["auth-a"] = AccountSnapshot{AuthID: "auth-a", LastError: "", Quota: QuotaSnapshot{FiveHour: QuotaWindow{Present: true, UsedPercent: 95}, Weekly: QuotaWindow{Present: true, UsedPercent: 20}}}
	// 将 auth-a 的实时查询结果单独保持为超限，用账号独立规则验证过滤。
	service.overrides["auth-a"] = AccountOverride{Settings: AccountSettings{MaxConcurrencyPerAccount: 2, QueueTimeoutSeconds: 0, FiveHour: WindowRule{Enabled: true, CutoffPercent: 10}, Weekly: WindowRule{Enabled: false}, MatchPolicy: matchAny}}
	response, err := service.Pick(pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-5", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex", Priority: 10}, {ID: "auth-b", Provider: "codex", Priority: 10}}})
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
	response, err := service.Pick(pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex", Priority: 10}, {ID: "auth-b", Provider: "codex", Priority: 10}}})
	if err != nil || response.AuthID != "auth-b" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestSchedulerUsesLowerPriorityAccountWhenHigherPriorityIsFull(t *testing.T) {
	host := &fakeHostClient{entries: []pluginapi.HostAuthFileEntry{{ID: "auth-high", AuthIndex: "index-high", Provider: "codex", Priority: 100}, {ID: "auth-free", AuthIndex: "index-free", Provider: "codex", Priority: 10}}, authJSON: json.RawMessage(`{"access_token":"token"}`), httpResult: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}`)}}
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.limiter.Acquire("r1", "auth-high", 2, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.limiter.Acquire("r2", "auth-high", 2, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.limiter.Acquire("r3", "auth-high", 3, 0); err != nil {
		t.Fatal(err)
	}
	response, err := service.Pick(pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-high", Provider: "codex", Priority: 100}, {ID: "auth-free", Provider: "codex", Priority: 10}}})
	if err != nil || response.AuthID != "auth-free" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
