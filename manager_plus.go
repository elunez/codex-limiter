package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type managerPlusQueryAccount struct {
	RowKey   string                   `json:"row_key"`
	Provider string                   `json:"provider"`
	Account  managerPlusQueryIdentity `json:"account"`
}

type managerPlusQueryIdentity struct {
	AccountSnapshot       string `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot     string `json:"auth_label_snapshot,omitempty"`
	AuthFileSnapshot      string `json:"auth_file_snapshot,omitempty"`
	AuthProviderSnapshot  string `json:"auth_provider_snapshot,omitempty"`
	AuthAccountIDSnapshot string `json:"auth_account_id_snapshot,omitempty"`
	AuthProjectIDSnapshot string `json:"auth_project_id_snapshot,omitempty"`
	AuthIndex             string `json:"auth_index"`
	Source                string `json:"source,omitempty"`
}

type managerPlusQueryResponse struct {
	Items []struct {
		RowKey  string `json:"row_key"`
		Windows []struct {
			ProviderWindowID string   `json:"provider_window_id"`
			WindowKind       string   `json:"window_kind"`
			UsedPercent      *float64 `json:"used_percent"`
			CycleEndMS       *int64   `json:"cycle_end_ms"`
			ObservedAtMS     int64    `json:"observed_at_ms"`
			DurationSeconds  int64    `json:"duration_seconds"`
			PlanType         string   `json:"plan_type"`
			Stale            bool     `json:"stale"`
			Availability     string   `json:"availability"`
		} `json:"windows"`
	} `json:"items"`
}

func fetchManagerPlusQuotas(host HostClient, auths []pluginapi.HostAuthFileEntry, settings GlobalSettings, now time.Time) (map[string]QuotaSnapshot, map[string]string) {
	results := make(map[string]QuotaSnapshot)
	errorsByID := make(map[string]string)
	accounts := make([]managerPlusQueryAccount, 0, len(auths))
	for _, auth := range auths {
		if strings.TrimSpace(auth.AuthIndex) == "" {
			errorsByID[auth.ID] = "账号缺少 auth_index"
			continue
		}
		accountSnapshot := auth.Email
		authAccountIDSnapshot := ""
		if credential, err := host.GetAuth(auth.AuthIndex); err == nil {
			credentialAccountID, credentialEmail := managerPlusCredentialIdentity(credential.JSON)
			authAccountIDSnapshot = credentialAccountID
			if strings.TrimSpace(accountSnapshot) == "" {
				accountSnapshot = credentialEmail
			}
		}
		accounts = append(accounts, managerPlusQueryAccount{
			RowKey:   auth.ID,
			Provider: "codex",
			Account: managerPlusQueryIdentity{
				AccountSnapshot:       accountSnapshot,
				AuthLabelSnapshot:     auth.Label,
				AuthFileSnapshot:      auth.Name,
				AuthProviderSnapshot:  auth.Provider,
				AuthAccountIDSnapshot: authAccountIDSnapshot,
				AuthIndex:             auth.AuthIndex,
				Source:                auth.Name,
			},
		})
	}
	if len(accounts) == 0 {
		return results, errorsByID
	}
	payload, _ := json.Marshal(map[string]any{
		"accounts":         accounts,
		"now_ms":           now.UnixMilli(),
		"include_inactive": false,
	})
	response, err := host.Do(pluginapi.HTTPRequest{
		Method: http.MethodPost,
		URL:    strings.TrimRight(settings.ManagerPlusBaseURL, "/") + "/v0/management/quota-snapshots/query",
		Headers: http.Header{
			"Authorization": []string{"Bearer " + settings.ManagerPlusManagementKey},
			"Content-Type":  []string{"application/json"},
		},
		Body: payload,
	})
	if err != nil {
		markManagerPlusError(auths, errorsByID, fmt.Sprintf("CPA Manager Plus 查询失败: %v", err))
		return results, errorsByID
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		markManagerPlusError(auths, errorsByID, fmt.Sprintf("CPA Manager Plus 返回 HTTP %d", response.StatusCode))
		return results, errorsByID
	}
	var decoded managerPlusQueryResponse
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		markManagerPlusError(auths, errorsByID, fmt.Sprintf("解析 CPA Manager Plus 额度失败: %v", err))
		return results, errorsByID
	}
	for _, item := range decoded.Items {
		snapshot := QuotaSnapshot{}
		stale := false
		for _, raw := range item.Windows {
			if raw.Availability == "inactive" {
				continue
			}
			if raw.Stale {
				stale = true
				continue
			}
			if raw.UsedPercent == nil || *raw.UsedPercent < 0 || *raw.UsedPercent > 100 {
				continue
			}
			window := QuotaWindow{Present: true, UsedPercent: *raw.UsedPercent, WindowSecs: raw.DurationSeconds, ObservedAt: time.UnixMilli(raw.ObservedAtMS)}
			if raw.CycleEndMS != nil && *raw.CycleEndMS > 0 {
				window.ResetAt = time.UnixMilli(*raw.CycleEndMS)
			}
			if snapshot.PlanType == "" {
				snapshot.PlanType = strings.ToLower(strings.TrimSpace(raw.PlanType))
			}
			kind := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw.WindowKind), "-", "_"))
			providerID := strings.ToLower(strings.TrimSpace(raw.ProviderWindowID))
			switch {
			case providerID == "five-hour" || kind == "five_hour" || raw.DurationSeconds == fiveHourSecs:
				snapshot.FiveHour = window
			case providerID == "weekly" || kind == "weekly" || raw.DurationSeconds == weeklySecs:
				snapshot.Weekly = window
			}
		}
		if !snapshot.FiveHour.Present && !snapshot.Weekly.Present {
			if stale {
				errorsByID[item.RowKey] = "CPA Manager Plus 额度快照已过期"
			} else {
				errorsByID[item.RowKey] = "CPA Manager Plus 尚无可用额度快照"
			}
			continue
		}
		results[item.RowKey] = snapshot
	}
	for _, auth := range auths {
		if _, ok := results[auth.ID]; !ok && errorsByID[auth.ID] == "" {
			errorsByID[auth.ID] = "CPA Manager Plus 未返回该账号额度"
		}
	}
	return results, errorsByID
}

func managerPlusCredentialIdentity(raw json.RawMessage) (accountID, email string) {
	var root map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &root) != nil {
		return "", ""
	}
	accountID = firstString(root, "account_id", "chatgpt_account_id", "accountId", "chatgptAccountId")
	email = firstString(root, "email", "account_snapshot", "accountSnapshot")
	for _, key := range []string{"tokens", "credentials", "auth", "oauth", "session"} {
		var nested map[string]json.RawMessage
		if value, ok := root[key]; !ok || json.Unmarshal(value, &nested) != nil {
			continue
		}
		if accountID == "" {
			accountID = firstString(nested, "account_id", "chatgpt_account_id", "accountId", "chatgptAccountId")
		}
		if email == "" {
			email = firstString(nested, "email", "account_snapshot", "accountSnapshot")
		}
	}
	return strings.TrimSpace(accountID), strings.TrimSpace(email)
}

func markManagerPlusError(auths []pluginapi.HostAuthFileEntry, target map[string]string, message string) {
	if message == "" {
		message = errors.New("CPA Manager Plus 查询失败").Error()
	}
	for _, auth := range auths {
		target[auth.ID] = message
	}
}
