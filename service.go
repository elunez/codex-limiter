package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	selectedAuthMetadataKey      = "selected_auth_id"
	selectedAuthIndexMetadataKey = "selected_auth_index"
)

type HostClient interface {
	ListAuths() ([]pluginapi.HostAuthFileEntry, error)
	GetAuthRuntime(authIndex string) (pluginapi.HostAuthGetRuntimeResponse, error)
	GetAuth(authIndex string) (pluginapi.HostAuthGetResponse, error)
	Do(request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)
}

type authClassifier interface {
	IsCodex(authID, authIndex string) (bool, error)
}

type hostAuthClassifier struct {
	host  HostClient
	mu    sync.RWMutex
	cache map[string]bool
}

func newHostAuthClassifier(host HostClient) *hostAuthClassifier {
	return &hostAuthClassifier{host: host, cache: make(map[string]bool)}
}

func (c *hostAuthClassifier) IsCodex(authID, authIndex string) (bool, error) {
	c.mu.RLock()
	value, cached := c.cache[authID]
	c.mu.RUnlock()
	if cached {
		return value, nil
	}
	if authIndex != "" {
		response, err := c.host.GetAuthRuntime(authIndex)
		if err == nil {
			isCodex := strings.EqualFold(strings.TrimSpace(response.Auth.Provider), "codex")
			c.remember(authID, isCodex)
			return isCodex, nil
		}
	}
	entries, err := c.host.ListAuths()
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.ID != authID && (authIndex == "" || entry.AuthIndex != authIndex) {
			continue
		}
		isCodex := strings.EqualFold(strings.TrimSpace(entry.Provider), "codex")
		c.remember(authID, isCodex)
		return isCodex, nil
	}
	return false, nil
}

func (c *hostAuthClassifier) remember(authID string, isCodex bool) {
	if authID == "" {
		return
	}
	c.mu.Lock()
	c.cache[authID] = isCodex
	c.mu.Unlock()
}

type AccountSnapshot struct {
	AuthID      string        `json:"auth_id"`
	AuthIndex   string        `json:"auth_index"`
	Name        string        `json:"name"`
	Email       string        `json:"email,omitempty"`
	Label       string        `json:"label,omitempty"`
	Plan        string        `json:"plan,omitempty"`
	Priority    int           `json:"priority"`
	Disabled    bool          `json:"disabled"`
	Unavailable bool          `json:"unavailable"`
	Quota       QuotaSnapshot `json:"quota"`
	LastSuccess time.Time     `json:"last_success,omitempty"`
	LastAttempt time.Time     `json:"last_attempt,omitempty"`
	LastError   string        `json:"last_error,omitempty"`
}

type Service struct {
	mu         sync.RWMutex
	cfg        Config
	settings   GlobalSettings
	overrides  map[string]AccountOverride
	accounts   map[string]AccountSnapshot
	host       HostClient
	limiter    *accountLimiter
	classifier authClassifier
	rr         map[string]uint64
	refreshMu  sync.Mutex
}

func NewService(cfg Config, host HostClient) (*Service, error) {
	state, err := loadState(cfg.StatePath, cfg.Defaults)
	if err != nil {
		return nil, err
	}
	return &Service{
		cfg:        cfg,
		settings:   state.Settings,
		overrides:  state.Overrides,
		accounts:   make(map[string]AccountSnapshot),
		host:       host,
		limiter:    newAccountLimiter(state.Settings.MaxConcurrencyPerAccount),
		classifier: newHostAuthClassifier(host),
		rr:         make(map[string]uint64),
	}, nil
}

func (s *Service) Reconfigure(cfg Config) error {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	s.limiter.Wake()
	return nil
}

func (s *Service) Stop() {
	if s != nil {
		s.limiter.Stop()
	}
}

func (s *Service) InterceptAfterAuth(request pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	authID := metadataString(request.Metadata, selectedAuthMetadataKey)
	if authID == "" || request.RequestID == "" {
		return pluginapi.RequestInterceptResponse{}
	}
	authIndex := metadataString(request.Metadata, selectedAuthIndexMetadataKey)
	isCodex, err := s.classifier.IsCodex(authID, authIndex)
	if err != nil || !isCodex {
		return pluginapi.RequestInterceptResponse{}
	}
	s.mu.RLock()
	enabled, effective := s.accountControlLocked(authID)
	s.mu.RUnlock()
	if !enabled {
		return pluginapi.RequestInterceptResponse{}
	}
	timeout := time.Duration(effective.QueueTimeoutSeconds) * time.Second
	if err := s.limiter.Acquire(request.RequestID, authID, effective.MaxConcurrencyPerAccount, timeout); err != nil {
		return limitErrorResponse(err)
	}
	return pluginapi.RequestInterceptResponse{}
}

func (s *Service) Complete(request pluginapi.RequestCompletion) {
	if request.RequestID != "" {
		s.limiter.Release(request.RequestID)
	}
}

func (s *Service) effectiveSettingsLocked(authID string) AccountSettings {
	if override, ok := s.overrides[authID]; ok && !override.UseGlobal {
		return override.Settings
	}
	return accountSettingsFromGlobal(s.settings)
}

// accountControlLocked 返回账号是否启用了调度控制及其有效规则。
// 未启用的账号仍由宿主正常调度，不受本插件的并发和额度限制。
func (s *Service) accountControlLocked(authID string) (bool, AccountSettings) {
	override, ok := s.overrides[authID]
	if !ok || override.UseGlobal {
		return false, accountSettingsFromGlobal(s.settings)
	}
	return true, override.Settings
}

func (s *Service) EffectiveSettings(authID string) AccountSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effectiveSettingsLocked(authID)
}

func (s *Service) SaveGlobalSettings(settings GlobalSettings) error {
	s.mu.Lock()
	if settings.ManagerPlusBaseURL == "" && s.settings.ManagerPlusBaseURL != "" {
		settings.ManagerPlusBaseURL = s.settings.ManagerPlusBaseURL
	}
	if settings.ManagerPlusManagementKey == "" && s.settings.ManagerPlusManagementKey != "" {
		settings.ManagerPlusManagementKey = s.settings.ManagerPlusManagementKey
	}
	normalized, err := normalizeGlobalSettings(settings)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	previous := s.settings
	s.settings = normalized
	err = s.persistLocked()
	if err != nil {
		s.settings = previous
	}
	s.mu.Unlock()
	if err == nil {
		s.limiter.Wake()
	}
	return err
}

func (s *Service) SaveAccountOverride(authID string, override AccountOverride) error {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return errors.New("auth_id is required")
	}
	if !override.UseGlobal {
		normalized, err := normalizeAccountSettings(override.Settings)
		if err != nil {
			return err
		}
		override.Settings = normalized
	}
	s.mu.Lock()
	previous, existed := s.overrides[authID]
	if override.UseGlobal {
		delete(s.overrides, authID)
	} else {
		s.overrides[authID] = override
	}
	err := s.persistLocked()
	if err != nil {
		if existed {
			s.overrides[authID] = previous
		} else {
			delete(s.overrides, authID)
		}
	}
	s.mu.Unlock()
	if err == nil {
		s.limiter.Wake()
	}
	return err
}

func (s *Service) persistLocked() error {
	return writeState(s.cfg.StatePath, persistedState{
		Version:   persistedStateVersion,
		Settings:  s.settings,
		Overrides: s.overrides,
	})
}

func (s *Service) RefreshAccounts(ids map[string]struct{}) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	auths, err := s.host.ListAuths()
	if err != nil {
		return err
	}
	selected := make([]pluginapi.HostAuthFileEntry, 0, len(auths))
	for _, auth := range auths {
		if !strings.EqualFold(auth.Provider, "codex") || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		if ids != nil {
			if _, ok := ids[auth.ID]; !ok {
				continue
			}
		}
		selected = append(selected, auth)
	}
	s.mu.RLock()
	settings := s.settings
	s.mu.RUnlock()
	now := time.Now()
	quotas := make(map[string]QuotaSnapshot)
	errorsByID := make(map[string]string)
	if settings.QuotaSource == quotaSourceManagerPlus {
		quotas, errorsByID = fetchManagerPlusQuotas(s.host, selected, settings, now)
	} else {
		type result struct {
			auth  pluginapi.HostAuthFileEntry
			quota QuotaSnapshot
			err   error
		}
		results := make(chan result, len(selected))
		semaphore := make(chan struct{}, 4)
		var workers sync.WaitGroup
		for _, auth := range selected {
			auth := auth
			workers.Add(1)
			go func() {
				defer workers.Done()
				semaphore <- struct{}{}
				quota, queryErr := fetchRealtimeQuota(s.host, auth, now)
				<-semaphore
				results <- result{auth: auth, quota: quota, err: queryErr}
			}()
		}
		workers.Wait()
		close(results)
		for result := range results {
			if result.err != nil {
				errorsByID[result.auth.ID] = result.err.Error()
			} else {
				quotas[result.auth.ID] = result.quota
			}
		}
	}
	s.mu.Lock()
	for _, auth := range selected {
		account := s.accounts[auth.ID]
		account.AuthID = auth.ID
		account.AuthIndex = auth.AuthIndex
		account.Name = auth.Name
		account.Email = auth.Email
		account.Label = auth.Label
		account.Priority = auth.Priority
		account.Disabled = auth.Disabled
		account.Unavailable = auth.Unavailable
		account.Plan = strings.ToLower(strings.TrimSpace(auth.AccountType))
		account.LastAttempt = now
		if quota, ok := quotas[auth.ID]; ok {
			account.Quota = quota
			if quota.PlanType != "" {
				account.Plan = quota.PlanType
			}
			account.LastSuccess = now
			account.LastError = ""
		} else {
			account.LastError = errorsByID[auth.ID]
		}
		s.accounts[auth.ID] = account
	}
	if ids == nil {
		seen := make(map[string]struct{}, len(selected))
		for _, auth := range selected {
			seen[auth.ID] = struct{}{}
		}
		for authID := range s.accounts {
			if _, ok := seen[authID]; !ok {
				delete(s.accounts, authID)
			}
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) Snapshot() (GlobalSettings, map[string]AccountOverride, []AccountSnapshot) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	overrides := make(map[string]AccountOverride, len(s.overrides))
	for key, value := range s.overrides {
		overrides[key] = value
	}
	accounts := make([]AccountSnapshot, 0, len(s.accounts))
	for _, account := range s.accounts {
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Priority != accounts[j].Priority {
			return accounts[i].Priority > accounts[j].Priority
		}
		left := strings.ToLower(accounts[i].Email + accounts[i].Name + accounts[i].AuthID)
		right := strings.ToLower(accounts[j].Email + accounts[j].Name + accounts[j].AuthID)
		return left < right
	})
	return s.settings, overrides, accounts
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func limitErrorResponse(err error) pluginapi.RequestInterceptResponse {
	status := http.StatusTooManyRequests
	message := "该 Codex 账号的并发请求已达到上限，排队等待超时，请稍后重试。"
	code := "account_concurrency_limit"
	if errors.Is(err, errStopped) {
		status = http.StatusServiceUnavailable
		message = "Codex 调度控制插件正在停止，请稍后重试。"
		code = "concurrency_limiter_stopped"
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "concurrency_limit_error", "code": code},
	})
	return pluginapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      status,
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}, "Retry-After": []string{"1"}},
		ResponseBody:    body,
	}
}

func (s *Service) accountDecision(account AccountSnapshot, effective AccountSettings, now time.Time) QuotaDecision {
	if account.Disabled || account.Unavailable {
		return QuotaDecision{Blocked: true, Reason: "账号当前不可用"}
	}
	return evaluateQuota(account.Quota, account.LastError, effective, effective.QueryFailurePolicy, now)
}

func (s *Service) requireAccount(authID string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.accounts[authID]; !ok {
		return fmt.Errorf("account %s not found", authID)
	}
	return nil
}
