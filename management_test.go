package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestManagementRegistrationAndPage(t *testing.T) {
	registration := registerManagement()
	if len(registration.Resources) != 1 || registration.Resources[0].Menu != "调度控制" {
		t.Fatalf("resources = %+v", registration.Resources)
	}
	for _, text := range []string{"Codex 调度控制", "启用调度控制", "额度查询失败时", "实时查询", "账号调度状态", "后台每 30 秒刷新额度，空闲 30 分钟后休眠", "会话粘性", "粘性时间", "session_affinity_enabled", "savedManagementKey", "plugin-version", managerPlusOriginHeader, managerPlusKeyHeader, ">序号<", ">并发数<", ">排队数<", ">停止调度<", "5 条/页", "account-page-jump", "request('/runtime')", "setInterval(pollRuntime,2000)"} {
		if !strings.Contains(statusPageHTML, text) {
			t.Fatalf("page missing %q", text)
		}
	}
	foundRuntime := false
	for _, route := range registration.Routes {
		if route.Method == http.MethodGet && route.Path == managementBasePath+"/runtime" {
			foundRuntime = true
		}
	}
	if !foundRuntime {
		t.Fatal("runtime status route is not registered")
	}
	if strings.Contains(statusPageHTML, "CPA Manager Plus 已连接") {
		t.Fatal("realtime page contains connection banner")
	}
	for _, removed := range []string{`id="g-manager-key"`, `id="g-manager-url"`, `id="g-enabled"`, `id="g-limit"`, `id="g-timeout"`, `id="g-five-enabled"`, `id="g-week-enabled"`, `id="g-match"`, `id="g-failure"`, "使用独立设置", "额度已停止"} {
		if strings.Contains(statusPageHTML, removed) {
			t.Fatalf("page still contains removed UI %q", removed)
		}
	}
}

func TestSaveSessionAffinitySettings(t *testing.T) {
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	target, err := NewService(cfg, &fakeHostClient{})
	if err != nil {
		t.Fatal(err)
	}
	serviceMu.Lock()
	previous := service
	service = target
	serviceMu.Unlock()
	defer func() {
		serviceMu.Lock()
		service = previous
		serviceMu.Unlock()
		target.Stop()
	}()

	raw, err := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management" + managementBasePath + "/settings",
		Body:   json.RawMessage(`{"quota_source":"realtime","session_affinity_enabled":false,"session_affinity_ttl_seconds":7200}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleManagement(raw); err != nil {
		t.Fatal(err)
	}
	saved, _, _ := target.Snapshot()
	if saved.SessionAffinityEnabled || saved.SessionAffinityTTLSeconds != 7200 {
		t.Fatalf("session affinity settings = %+v", saved)
	}
}

func TestSaveQuotaSourcePreservesLegacyDefaults(t *testing.T) {
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	target, err := NewService(cfg, &fakeHostClient{})
	if err != nil {
		t.Fatal(err)
	}
	serviceMu.Lock()
	previous := service
	service = target
	serviceMu.Unlock()
	defer func() {
		serviceMu.Lock()
		service = previous
		serviceMu.Unlock()
		target.Stop()
	}()

	raw, err := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodPut, Path: "/v0/management" + managementBasePath + "/settings", Body: json.RawMessage(`{"quota_source":"realtime"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleManagement(raw); err != nil {
		t.Fatal(err)
	}
	saved, _, _ := target.Snapshot()
	if saved.MaxConcurrencyPerAccount != 3 || saved.QueueTimeoutSeconds != 300 || saved.FiveHour.CutoffPercent != 95 || saved.QueryFailurePolicy != failureAllow {
		t.Fatalf("legacy defaults changed: %+v", saved)
	}
}

func TestSaveManagerPlusSettingsUsesCurrentManagementCredential(t *testing.T) {
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	target, err := NewService(cfg, &fakeHostClient{})
	if err != nil {
		t.Fatal(err)
	}
	serviceMu.Lock()
	previous := service
	service = target
	serviceMu.Unlock()
	defer func() {
		serviceMu.Lock()
		service = previous
		serviceMu.Unlock()
		target.Stop()
	}()

	settings := defaultSettings()
	settings.QuotaSource = quotaSourceManagerPlus
	settings.ManagerPlusBaseURL = "http://127.0.0.1:8318"
	body, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management" + managementBasePath + "/settings",
		Headers: http.Header{
			"Authorization":         []string{"Bearer cpa-management-key"},
			"Origin":                []string{"http://127.0.0.1:8317"},
			managerPlusKeyHeader:    []string{"manager-admin-key"},
			managerPlusOriginHeader: []string{"http://127.0.0.1:8318"},
		},
		Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleManagement(raw); err != nil {
		t.Fatal(err)
	}
	saved, _, _ := target.Snapshot()
	if saved.ManagerPlusManagementKey != "manager-admin-key" {
		t.Fatalf("management credential was not reused: %q", saved.ManagerPlusManagementKey)
	}
	if saved.ManagerPlusBaseURL != "http://127.0.0.1:8318" {
		t.Fatalf("management origin was not reused: %q", saved.ManagerPlusBaseURL)
	}
}

func TestStatusRefreshesManagerPlusCredentialFromPageContext(t *testing.T) {
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	host := &capturingHostClient{fakeHostClient: fakeHostClient{
		entries: []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex"}},
		httpResult: pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"items":[]}`),
		},
	}}
	target, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	settings := defaultSettings()
	settings.QuotaSource = quotaSourceManagerPlus
	settings.ManagerPlusBaseURL = "http://old-manager.example"
	settings.ManagerPlusManagementKey = "old-key"
	if err := target.SaveGlobalSettings(settings); err != nil {
		t.Fatal(err)
	}
	serviceMu.Lock()
	previous := service
	service = target
	serviceMu.Unlock()
	defer func() {
		serviceMu.Lock()
		service = previous
		serviceMu.Unlock()
		target.Stop()
	}()

	raw, err := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management" + managementBasePath + "/status",
		Headers: http.Header{
			"Authorization":         []string{"Bearer cpa-management-key"},
			managerPlusKeyHeader:    []string{"manager-admin-key"},
			managerPlusOriginHeader: []string{"http://manager.example"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleManagement(raw); err != nil {
		t.Fatal(err)
	}
	saved, _, _ := target.Snapshot()
	if saved.ManagerPlusBaseURL != "http://manager.example" || saved.ManagerPlusManagementKey != "manager-admin-key" {
		t.Fatalf("manager context was not synchronized: %+v", saved)
	}
	if host.request.URL != "http://manager.example/v0/management/quota-snapshots/query" || host.request.Headers.Get("Authorization") != "Bearer manager-admin-key" {
		t.Fatalf("quota request used stale manager context: %+v", host.request)
	}
}

func TestRuntimeStatusDoesNotQueryQuota(t *testing.T) {
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	host := &capturingHostClient{fakeHostClient: fakeHostClient{
		entries: []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex"}},
		httpResult: pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"items":[]}`),
		},
	}}
	target, err := NewService(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	settings := defaultSettings()
	settings.QuotaSource = quotaSourceManagerPlus
	settings.ManagerPlusBaseURL = "http://manager.example"
	settings.ManagerPlusManagementKey = "manager-key"
	if err := target.SaveGlobalSettings(settings); err != nil {
		t.Fatal(err)
	}
	target.mu.Lock()
	target.accounts["auth-a"] = AccountSnapshot{AuthID: "auth-a", AuthIndex: "index-a", Email: "member@example.com"}
	target.mu.Unlock()
	serviceMu.Lock()
	previous := service
	service = target
	serviceMu.Unlock()
	defer func() {
		serviceMu.Lock()
		service = previous
		serviceMu.Unlock()
		target.Stop()
	}()

	raw, err := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management" + managementBasePath + "/runtime",
		Headers: http.Header{
			managerPlusKeyHeader:    []string{"new-manager-key"},
			managerPlusOriginHeader: []string{"http://new-manager.example"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleManagement(raw); err != nil {
		t.Fatal(err)
	}
	if len(host.requests) != 0 {
		t.Fatalf("runtime status unexpectedly queried quota: %+v", host.requests)
	}
	saved, _, accounts := target.Snapshot()
	if saved.ManagerPlusBaseURL != "http://manager.example" || saved.ManagerPlusManagementKey != "manager-key" {
		t.Fatalf("runtime status unexpectedly changed manager context: %+v", saved)
	}
	if len(accounts) != 1 || accounts[0].AuthID != "auth-a" {
		t.Fatalf("runtime status changed account snapshot: %+v", accounts)
	}
}

func TestSaveSettingsAndAccountOverride(t *testing.T) {
	cfg := defaultConfig()
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")
	service, err := NewService(cfg, &fakeHostClient{})
	if err != nil {
		t.Fatal(err)
	}
	settings := defaultSettings()
	settings.MaxConcurrencyPerAccount = 3
	if err := service.SaveGlobalSettings(settings); err != nil {
		t.Fatal(err)
	}
	override := AccountOverride{Settings: AccountSettings{MaxConcurrencyPerAccount: 1, QueueTimeoutSeconds: 60, FiveHour: WindowRule{Enabled: true, CutoffPercent: 80}, Weekly: WindowRule{Enabled: true, CutoffPercent: 85}, MatchPolicy: matchAny}}
	if err := service.SaveAccountOverride("auth-a", override); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewService(cfg, &fakeHostClient{})
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.EffectiveSettings("auth-a"); got.MaxConcurrencyPerAccount != 1 || got.Weekly.CutoffPercent != 85 {
		t.Fatalf("effective = %+v", got)
	}
	if got := reloaded.EffectiveSettings("auth-b"); got.MaxConcurrencyPerAccount != 3 {
		t.Fatalf("global = %+v", got)
	}
}
