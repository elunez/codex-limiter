package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseRealtimeQuota(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	raw := []byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":78,"limit_window_seconds":18000,"reset_after_seconds":3600},"secondary_window":{"used_percent":64,"limit_window_seconds":604800,"reset_at":1800086400}}}`)
	quota, err := parseRealtimeQuota(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if quota.PlanType != "plus" || quota.FiveHour.UsedPercent != 78 || quota.Weekly.UsedPercent != 64 {
		t.Fatalf("quota = %+v", quota)
	}
	if !quota.FiveHour.ResetAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("reset = %v", quota.FiveHour.ResetAt)
	}
}

func TestParseAuthMaterialSupportsNestedTokens(t *testing.T) {
	material, err := parseAuthMaterial(json.RawMessage(`{"tokens":{"access_token":"token-a"},"chatgpt_account_id":"account-a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if material.AccessToken != "token-a" || material.AccountID != "account-a" {
		t.Fatalf("material = %+v", material)
	}
}

func TestEvaluateQuotaAnyAndFailurePolicies(t *testing.T) {
	now := time.Now()
	settings := AccountSettings{MaxConcurrencyPerAccount: 2, FiveHour: WindowRule{Enabled: true, CutoffPercent: 90}, Weekly: WindowRule{Enabled: true, CutoffPercent: 90}, MatchPolicy: matchAny}
	quota := QuotaSnapshot{FiveHour: QuotaWindow{Present: true, UsedPercent: 80, ResetAt: now.Add(time.Hour)}, Weekly: QuotaWindow{Present: true, UsedPercent: 93, ResetAt: now.Add(time.Hour)}}
	if decision := evaluateQuota(quota, "", settings, failureAllow, now); !decision.Blocked {
		t.Fatalf("decision = %+v", decision)
	}
	if decision := evaluateQuota(QuotaSnapshot{}, "query failed", settings, failureAllow, now); decision.Blocked || !decision.Unknown {
		t.Fatalf("allow decision = %+v", decision)
	}
	if decision := evaluateQuota(QuotaSnapshot{}, "query failed", settings, failureDeny, now); !decision.Blocked {
		t.Fatalf("deny decision = %+v", decision)
	}
}
