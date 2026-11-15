package helps

import (
	"net/http"
	"reflect"
	"testing"
	"time"
)

// Recorded usage responses with identities removed.
const (
	recordedCodexUsage  = `{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":9,"limit_window_seconds":604800,"reset_after_seconds":505219,"reset_at":1791580412},"secondary_window":null},"code_review_rate_limit":null,"additional_rate_limits":[{"limit_name":"gpt-reserve","metered_feature":"base_model_inference","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_after_seconds":604800,"reset_at":1791679994},"secondary_window":null}}],"credits":{"unlimited":false,"overage_limit_reached":false},"rate_limit_reached_type":null}`
	recordedClaudeUsage = `{"five_hour":{"utilization":14.0,"resets_at":"2026-10-04T02:40:00.347417+00:00"},"seven_day":{"utilization":31.0,"resets_at":"2026-10-08T23:00:00.347435+00:00"},"iguana_necktie":{"utilization":5.3976432,"resets_at":"2026-11-05T07:59:00+00:00"},"extra_usage":null,"spend":null,"member_dashboard_available":false}`
)

func TestParseCodexUsageHeadersMapsRecordedResponse(t *testing.T) {
	got := ParseCodexUsageHeaders([]byte(recordedCodexUsage))
	want := http.Header{}
	want.Set("X-Codex-Primary-Used-Percent", "9")
	want.Set("X-Codex-Primary-Window-Minutes", "10080")
	want.Set("X-Codex-Primary-Reset-After-Seconds", "505219")
	want.Set("X-Codex-Primary-Reset-At", "1791580412")
	want.Set("X-Codex-Limit-Reached", "false")
	// The null secondary window and the additional limits map to nothing.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodexUsageHeaders() = %v, want %v", got, want)
	}
}

func TestParseCodexUsageHeadersKeepsExhaustedWindowAsGiven(t *testing.T) {
	body := `{"rate_limit":{"limit_reached":true,"primary_window":{"used_percent":100.4,"reset_after_seconds":3600,"reset_at":1791580412},"secondary_window":null}}`
	got := ParseCodexUsageHeaders([]byte(body))
	want := http.Header{}
	want.Set("X-Codex-Primary-Used-Percent", "100.4")
	want.Set("X-Codex-Primary-Reset-After-Seconds", "3600")
	want.Set("X-Codex-Primary-Reset-At", "1791580412")
	want.Set("X-Codex-Limit-Reached", "true")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodexUsageHeaders() = %v, want %v", got, want)
	}
}

func TestParseCodexUsageHeadersWithoutWindowReturnsNil(t *testing.T) {
	body := `{"rate_limit":{"limit_reached":true,"primary_window":null,"secondary_window":null}}`
	if got := ParseCodexUsageHeaders([]byte(body)); got != nil {
		t.Fatalf("ParseCodexUsageHeaders() = %v, want nil", got)
	}
}

func TestParseClaudeUsageHeadersMapsRecordedResponse(t *testing.T) {
	got := ParseClaudeUsageHeaders([]byte(recordedClaudeUsage))
	want := http.Header{}
	want.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.14")
	want.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1791081600")
	want.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.31")
	want.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1791500400")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseClaudeUsageHeaders() = %v, want %v", got, want)
	}
}

func TestParseClaudeUsageHeadersWithoutWindowReturnsNil(t *testing.T) {
	if got := ParseClaudeUsageHeaders([]byte(`{"five_hour":null,"seven_day":null}`)); got != nil {
		t.Fatalf("ParseClaudeUsageHeaders() = %v, want nil", got)
	}
}

func TestClaudeUsageCredentialQuotaAvailable(t *testing.T) {
	readStartedAt := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	window := func(utilization, resetsAt string) string {
		return `{"utilization":` + utilization + `,"resets_at":` + resetsAt + `}`
	}
	future := `"2026-10-08T23:00:00.347435+00:00"`
	low := window("5.0", future)
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"recorded", recordedClaudeUsage, true},
		{"live five hour idle", `{"five_hour":null,"seven_day":` + low + `,"seven_day_opus":null,"extra_usage":{},"limits":[]}`, true},
		{"seven day idle", `{"five_hour":` + low + `,"seven_day":null}`, true},
		{"zero usage", `{"five_hour":null,"seven_day":` + window("0", future) + `}`, true},
		{"both idle", `{"five_hour":null,"seven_day":null}`, false},
		{"five hour missing", `{"seven_day":` + low + `}`, false},
		{"seven day missing", `{"five_hour":null}`, false},
		{"empty window", `{"five_hour":{},"seven_day":` + low + `}`, false},
		{"hollow window", `{"five_hour":` + window("null", "null") + `,"seven_day":` + low + `}`, false},
		{"string window", `{"five_hour":"idle","seven_day":` + low + `}`, false},
		{"string utilization", `{"five_hour":null,"seven_day":` + window(`"5"`, future) + `}`, false},
		{"negative utilization", `{"five_hour":null,"seven_day":` + window("-1", future) + `}`, false},
		{"exhausted", `{"five_hour":null,"seven_day":` + window("100", future) + `}`, false},
		{"over limit", `{"five_hour":` + window("100.5", future) + `,"seven_day":` + low + `}`, false},
		{"infinite utilization", `{"five_hour":null,"seven_day":` + window("1e400", future) + `}`, false},
		{"missing reset", `{"five_hour":null,"seven_day":{"utilization":5}}`, false},
		{"bad reset", `{"five_hour":null,"seven_day":` + window("5", `"next week"`) + `}`, false},
		{"numeric reset", `{"five_hour":null,"seven_day":` + window("5", "1791500000") + `}`, false},
		{"reset before read", `{"five_hour":null,"seven_day":` + window("5", `"2026-09-30T23:00:00Z"`) + `}`, false},
		{"reset at read", `{"five_hour":null,"seven_day":` + window("5", `"2026-10-01T00:00:00Z"`) + `}`, false},
		{"array", `[]`, false},
		{"empty", ``, false},
		{"not json", `rate limited`, false},
		{"truncated", `{"five_hour":null,"seven_day":` + low, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeUsageCredentialQuotaAvailable([]byte(tc.body), readStartedAt); got != tc.want {
				t.Fatalf("ClaudeUsageCredentialQuotaAvailable(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
