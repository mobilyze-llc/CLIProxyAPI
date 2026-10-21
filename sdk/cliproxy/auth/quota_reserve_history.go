package auth

import (
	"net/http"
	"strings"
	"time"
)

type quotaReserveWindow struct {
	ObservedAt  time.Time `json:"observed_at"`
	ResetAt     time.Time `json:"reset_at,omitempty"`
	UsageKnown  bool      `json:"usage_known"`
	UsedPercent float64   `json:"used_percent"`
	Exhausted   bool      `json:"exhausted,omitempty"`
}

func (q QuotaState) reserveWindows(provider string) map[string]quotaReserveWindow {
	if q.ReserveWindows != nil {
		return q.ReserveWindows
	}
	q.observeReserveWindows(provider, q.ObservedAt)
	return q.ReserveWindows
}

func (q *QuotaState) observeReserveWindows(provider string, now time.Time) {
	if q.ObservedAt.IsZero() || len(q.Signals) == 0 {
		return
	}
	names := []string{"primary", "secondary"}
	if strings.EqualFold(provider, "claude") {
		names = []string{"5h", "7d"}
	}
	for i, window := range observedQuotaWindows(&Auth{Provider: provider, Quota: *q}) {
		if !window.observed && !window.exhausted {
			continue
		}
		if strings.EqualFold(provider, "codex") && window.resetAt.IsZero() && window.usageKnown && window.usedPercent == 0 && !window.exhausted &&
			q.Signals[http.CanonicalHeaderKey("X-Codex-"+names[i]+"-Window-Minutes")] == "0" {
			continue
		}
		if q.ReserveWindows == nil {
			q.ReserveWindows = make(map[string]quotaReserveWindow, 2)
		}
		previous, exists := q.ReserveWindows[names[i]]
		if previous.ObservedAt.After(q.ObservedAt) {
			continue
		}
		next := quotaReserveWindow{
			ObservedAt: q.ObservedAt, ResetAt: window.resetAt, UsageKnown: window.usageKnown,
			UsedPercent: window.usedPercent, Exhausted: window.exhausted,
		}
		if next.ResetAt.IsZero() {
			if exists && !next.Exhausted && (!next.UsageKnown || next.UsedPercent == 0) {
				continue
			}
			if exists && (previous.ResetAt.IsZero() || previous.ResetAt.After(now)) {
				next.ResetAt = previous.ResetAt
				if !previous.UsageKnown || previous.UsedPercent > next.UsedPercent || previous.Exhausted {
					next.UsageKnown = previous.UsageKnown
					next.UsedPercent = previous.UsedPercent
					next.Exhausted = next.Exhausted || previous.Exhausted
				}
			} else if next.UsedPercent == 0 && !next.Exhausted {
				next.UsageKnown = false
			}
		}
		q.ReserveWindows[names[i]] = next
	}
}

func mergeReserveWindows(target, source map[string]quotaReserveWindow) map[string]quotaReserveWindow {
	if len(source) == 0 {
		return target
	}
	merged := make(map[string]quotaReserveWindow, len(target)+len(source))
	for name, window := range target {
		merged[name] = window
	}
	for name, window := range source {
		if current, exists := merged[name]; !exists || !window.ObservedAt.Before(current.ObservedAt) {
			merged[name] = window
		}
	}
	return merged
}
