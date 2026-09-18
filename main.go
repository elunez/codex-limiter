package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static int call_host_api(cliproxy_host_api* host, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (host == NULL || host->call == NULL) {
		return 1;
	}
	return host->call(host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(cliproxy_host_api* host, void* ptr, size_t len) {
	if (host != NULL && host->free_buffer != NULL && ptr != NULL) {
		host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

var (
	hostAPI   atomic.Pointer[C.cliproxy_host_api]
	serviceMu sync.RWMutex
	service   *Service
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	hostAPI.Store(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", 0))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error(), 0))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	serviceMu.Lock()
	current := service
	service = nil
	serviceMu.Unlock()
	if current != nil {
		current.Stop()
	}
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configureService(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginQuiesce:
		if current := currentService(); current != nil {
			current.Stop()
		}
		return okEnvelope(struct{}{})
	case pluginabi.MethodSchedulerPick:
		var req pluginapi.SchedulerPickRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, fmt.Errorf("decode scheduler.pick: %w", err)
		}
		current := currentService()
		if current == nil {
			return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
		}
		response, err := current.Pick(req)
		if err != nil {
			return errorEnvelope("codex_account_scheduling", err.Error(), 429), nil
		}
		return okEnvelope(response)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(registerManagement())
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	case pluginabi.MethodRequestInterceptBefore:
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodRequestInterceptAfter:
		var req pluginapi.RequestInterceptRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, fmt.Errorf("decode request.intercept_after: %w", err)
		}
		current := currentService()
		if current == nil {
			return okEnvelope(pluginapi.RequestInterceptResponse{})
		}
		return okEnvelope(current.InterceptAfterAuth(req))
	case pluginabi.MethodRequestComplete:
		var completion pluginapi.RequestCompletion
		if err := json.Unmarshal(request, &completion); err != nil {
			return nil, fmt.Errorf("decode request.complete: %w", err)
		}
		if current := currentService(); current != nil {
			current.Complete(completion)
		}
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, 0), nil
	}
}

func configureService(raw []byte) error {
	var request lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &request); err != nil {
			return fmt.Errorf("decode lifecycle request: %w", err)
		}
	}
	cfg, err := decodeConfig(request.ConfigYAML)
	if err != nil {
		return err
	}

	serviceMu.Lock()
	defer serviceMu.Unlock()
	if service == nil {
		service, err = NewService(cfg, ABIHostClient{})
		if err != nil {
			return err
		}
		service.StartQuotaRefresh()
	} else {
		if err := service.Reconfigure(cfg); err != nil {
			return err
		}
	}
	return nil
}

func currentService() *Service {
	serviceMu.RLock()
	defer serviceMu.RUnlock()
	return service
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message, HTTPStatus: status}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

type ABIHostClient struct{}

func (ABIHostClient) ListAuths() ([]pluginapi.HostAuthFileEntry, error) {
	result, err := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if err != nil {
		return nil, err
	}
	var response struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, fmt.Errorf("decode host.auth.list: %w", err)
	}
	return response.Files, nil
}

func (ABIHostClient) GetAuthRuntime(authIndex string) (pluginapi.HostAuthGetRuntimeResponse, error) {
	result, err := callHost(pluginabi.MethodHostAuthGetRuntime, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if err != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, err
	}
	var response pluginapi.HostAuthGetRuntimeResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return response, fmt.Errorf("decode host.auth.get_runtime: %w", err)
	}
	return response, nil
}

func (ABIHostClient) GetAuth(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	result, err := callHost(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if err != nil {
		return pluginapi.HostAuthGetResponse{}, err
	}
	var response pluginapi.HostAuthGetResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return response, fmt.Errorf("decode host.auth.get: %w", err)
	}
	return response, nil
}

func (ABIHostClient) Do(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	result, err := callHost(pluginabi.MethodHostHTTPDo, request)
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	var response pluginapi.HTTPResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return response, fmt.Errorf("decode host.http.do: %w", err)
	}
	return response, nil
}

func callHost(method string, payload any) (json.RawMessage, error) {
	host := hostAPI.Load()
	if host == nil {
		return nil, fmt.Errorf("host callback %s unavailable", method)
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, err)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		allocated := C.CBytes(rawPayload)
		if allocated == nil {
			return nil, fmt.Errorf("allocate host callback %s", method)
		}
		defer C.free(allocated)
		requestPtr = (*C.uint8_t)(allocated)
	}
	callCode := C.call_host_api(host, cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(host, response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}
	var wrapped envelope
	if err := json.Unmarshal(rawResponse, &wrapped); err != nil {
		return nil, fmt.Errorf("decode host callback %s: %w", method, err)
	}
	if !wrapped.OK {
		if wrapped.Error != nil {
			return nil, fmt.Errorf("%s: %s", wrapped.Error.Code, wrapped.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), wrapped.Result...), nil
}
