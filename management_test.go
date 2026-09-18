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
	for _, text := range []string{"Codex 调度控制", "额度查询失败时", "实时查询", "账号调度状态", "自动读取额度快照", "savedManagementKey", ">排队<", ">停止调度<"} {
		if !strings.Contains(statusPageHTML, text) {
			t.Fatalf("page missing %q", text)
		}
	}
	if strings.Contains(statusPageHTML, "CPA Manager Plus 已连接") {
		t.Fatal("realtime page contains connection banner")
	}
	for _, removed := range []string{`id="g-manager-key"`, `id="g-manager-url"`, "额度已停止"} {
		if strings.Contains(statusPageHTML, removed) {
			t.Fatalf("page still contains removed UI %q", removed)
		}
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
		Method:  http.MethodPut,
		Path:    "/v0/management" + managementBasePath + "/settings",
		Headers: http.Header{"Authorization": []string{"Bearer remembered-secret"}, "Origin": []string{"http://127.0.0.1:8318"}},
		Body:    body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleManagement(raw); err != nil {
		t.Fatal(err)
	}
	saved, _, _ := target.Snapshot()
	if saved.ManagerPlusManagementKey != "remembered-secret" {
		t.Fatalf("management credential was not reused: %q", saved.ManagerPlusManagementKey)
	}
	if saved.ManagerPlusBaseURL != "http://127.0.0.1:8318" {
		t.Fatalf("management origin was not reused: %q", saved.ManagerPlusBaseURL)
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
