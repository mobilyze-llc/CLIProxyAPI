package helps

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

// CodexUsageURL is the ChatGPT backend usage endpoint the Codex CLI reads.
const CodexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// ClaudeUsageUserAgent returns the Claude Code User-Agent a usage read carries:
// the configured CLI baseline. Inference sends it for every request without a
// confirmed Claude Code client, and a stabilized device profile is accepted only
// at the baseline version, so a credential's resolved profile has it too.
func ClaudeUsageUserAgent(cfg *config.Config) string {
	return defaultClaudeDeviceProfile(cfg).UserAgent
}

// ParseCodexUsageHeaders converts a /backend-api/wham/usage response into the
// X-Codex primary and secondary window headers that Codex responses carry.
// Each non-null window's fields are copied as given, like the proxied-response
// path, because a usage read replaces the whole observation and a dropped
// window would erase its exhaustion. It returns nil when no window is present,
// so a snapshot holding only the limit-reached flag never erases the windows.
func ParseCodexUsageHeaders(body []byte) http.Header {
	rateLimit := gjson.GetBytes(body, "rate_limit")
	headers := make(http.Header)
	for _, name := range []string{"Primary", "Secondary"} {
		window := rateLimit.Get(strings.ToLower(name) + "_window")
		if !window.IsObject() {
			continue
		}
		prefix := "X-Codex-" + name + "-"
		if used := window.Get("used_percent"); used.Type == gjson.Number {
			headers.Set(prefix+"Used-Percent", used.Raw)
		}
		if seconds := window.Get("limit_window_seconds"); seconds.Type == gjson.Number {
			headers.Set(prefix+"Window-Minutes", strconv.FormatInt(seconds.Int()/60, 10))
		}
		if resetAfter := window.Get("reset_after_seconds"); resetAfter.Type == gjson.Number {
			headers.Set(prefix+"Reset-After-Seconds", resetAfter.Raw)
		}
		if resetAt := window.Get("reset_at"); resetAt.Type == gjson.Number {
			headers.Set(prefix+"Reset-At", resetAt.Raw)
		}
	}
	if len(headers) == 0 {
		return nil
	}
	setCodexQuotaScalarHeaderFromResult(headers, "X-Codex-Limit-Reached", rateLimit, "limit_reached")
	return headers
}

// ParseClaudeUsageHeaders converts an /api/oauth/usage response into the
// Anthropic unified 5h and 7d headers that Claude responses carry. Utilization
// arrives as a percent and leaves as a fraction; resets_at arrives as RFC 3339
// and leaves as Unix seconds. It returns nil when the response holds no usable
// window.
func ParseClaudeUsageHeaders(body []byte) http.Header {
	headers := make(http.Header)
	for name, path := range map[string]string{"5h": "five_hour", "7d": "seven_day"} {
		window := gjson.GetBytes(body, path)
		utilization := window.Get("utilization")
		resetAt, errParse := time.Parse(time.RFC3339, window.Get("resets_at").String())
		if utilization.Type != gjson.Number || errParse != nil {
			continue
		}
		prefix := "Anthropic-Ratelimit-Unified-" + name + "-"
		headers.Set(prefix+"Utilization", strconv.FormatFloat(utilization.Float()/100, 'f', -1, 64))
		headers.Set(prefix+"Reset", strconv.FormatInt(resetAt.Unix(), 10))
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}
