package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type staticClassifier bool

func (value staticClassifier) IsCodex(_, _ string) (bool, error) { return bool(value), nil }

type fakeHostClient struct {
	runtime    pluginapi.HostAuthGetRuntimeResponse
	entries    []pluginapi.HostAuthFileEntry
	authJSON   json.RawMessage
	httpResult pluginapi.HTTPResponse
	runtimeErr error
	listErr    error
	authErr    error
	httpErr    error
}

func (client *fakeHostClient) ListAuths() ([]pluginapi.HostAuthFileEntry, error) {
	return client.entries, client.listErr
}
func (client *fakeHostClient) GetAuthRuntime(string) (pluginapi.HostAuthGetRuntimeResponse, error) {
	return client.runtime, client.runtimeErr
}
func (client *fakeHostClient) GetAuth(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	return pluginapi.HostAuthGetResponse{AuthIndex: authIndex, JSON: client.authJSON}, client.authErr
}
func (client *fakeHostClient) Do(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return client.httpResult, client.httpErr
}

func testService(isCodex bool, limit, timeout int) *Service {
	settings := defaultSettings()
	settings.MaxConcurrencyPerAccount = limit
	settings.QueueTimeoutSeconds = timeout
	return &Service{settings: settings, overrides: make(map[string]AccountOverride), accounts: make(map[string]AccountSnapshot), limiter: newAccountLimiter(limit), classifier: staticClassifier(isCodex), rr: make(map[string]uint64)}
}

func interceptRequest(requestID, authID string) pluginapi.RequestInterceptRequest {
	return pluginapi.RequestInterceptRequest{RequestID: requestID, Metadata: map[string]any{selectedAuthMetadataKey: authID, selectedAuthIndexMetadataKey: "index-" + authID}}
}

func TestServiceLimitsCodexAccountAndReleasesOnCompletion(t *testing.T) {
	service := testService(true, 2, 0)
	if service.InterceptAfterAuth(interceptRequest("request-1", "auth-a")).Terminate {
		t.Fatal("first request terminated")
	}
	if service.InterceptAfterAuth(interceptRequest("request-2", "auth-a")).Terminate {
		t.Fatal("second request terminated")
	}
	response := service.InterceptAfterAuth(interceptRequest("request-3", "auth-a"))
	if !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("response = %+v", response)
	}
	service.Complete(pluginapi.RequestCompletion{RequestID: "request-1"})
	if service.InterceptAfterAuth(interceptRequest("request-3", "auth-a")).Terminate {
		t.Fatal("released slot was not reused")
	}
}

func TestServiceUsesAccountConcurrencyOverride(t *testing.T) {
	service := testService(true, 2, 0)
	service.overrides["auth-a"] = AccountOverride{Settings: AccountSettings{MaxConcurrencyPerAccount: 1, QueueTimeoutSeconds: 0, FiveHour: WindowRule{Enabled: true, CutoffPercent: 90}, Weekly: WindowRule{Enabled: true, CutoffPercent: 90}, MatchPolicy: matchAny}}
	if service.InterceptAfterAuth(interceptRequest("request-1", "auth-a")).Terminate {
		t.Fatal("first request terminated")
	}
	if !service.InterceptAfterAuth(interceptRequest("request-2", "auth-a")).Terminate {
		t.Fatal("override was not applied")
	}
}

func TestServiceIgnoresNonCodexAndDisabledPlugin(t *testing.T) {
	service := testService(false, 1, 0)
	if service.InterceptAfterAuth(interceptRequest("request-1", "auth-a")).Terminate {
		t.Fatal("non-Codex request terminated")
	}
	service = testService(true, 1, 0)
	service.settings.Enabled = false
	if service.InterceptAfterAuth(interceptRequest("request-1", "auth-a")).Terminate {
		t.Fatal("disabled plugin terminated request")
	}
}

func TestHostAuthClassifierFallsBackToAuthList(t *testing.T) {
	classifier := newHostAuthClassifier(&fakeHostClient{entries: []pluginapi.HostAuthFileEntry{{ID: "auth-a", AuthIndex: "index-a", Provider: "codex"}}, runtimeErr: errors.New("runtime unavailable")})
	isCodex, err := classifier.IsCodex("auth-a", "index-a")
	if err != nil || !isCodex {
		t.Fatalf("isCodex=%v err=%v", isCodex, err)
	}
}
