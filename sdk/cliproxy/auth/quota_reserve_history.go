package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type quotaReserveWindow struct {
	ObservedAt   time.Time `json:"observed_at"`
	ResetAt      time.Time `json:"reset_at,omitempty"`
	UsageTrusted bool      `json:"usage_trusted"`
	UsedPercent  float64   `json:"used_percent"`
}

func (q QuotaState) reserveWindows(provider string) map[string]quotaReserveWindow {
	if q.ReserveWindows == nil {
		q.observeReserveWindows(provider, q.Signals, q.ObservedAt)
	}
	return q.ReserveWindows
}

func (q *QuotaState) observeReserveWindows(provider string, signals map[string]string, observedAt time.Time) {
	if observedAt.IsZero() || len(signals) == 0 {
		return
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	signal := func(name string) string { return signals[http.CanonicalHeaderKey(name)] }
	names := []string{"primary", "secondary"}
	if provider == "claude" {
		names = []string{"5h", "7d"}
	}
	for i, window := range observedQuotaWindows(&Auth{Provider: provider, Quota: QuotaState{Signals: signals, ObservedAt: observedAt}}) {
		name := names[i]
		previous, exists := q.ReserveWindows[name]
		if previous.ObservedAt.After(observedAt) {
			continue
		}
		if provider == "codex" {
			minutes, errMinutes := strconv.ParseInt(signal("X-Codex-"+name+"-Window-Minutes"), 10, 64)
			if errMinutes == nil && minutes == 0 {
				delete(q.ReserveWindows, name)
				continue
			}
		}
		if !window.observed {
			continue
		}
		rejected := provider == "claude" && strings.EqualFold(signal("Anthropic-Ratelimit-Unified-"+name+"-Status"), "rejected")
		next := quotaReserveWindow{ObservedAt: observedAt, UsageTrusted: window.usageKnown}
		if window.usageKnown {
			next.UsedPercent = window.usedPercent
		}
		if _, errMarshal := window.resetAt.MarshalJSON(); errMarshal == nil {
			next.ResetAt = window.resetAt
		}
		if next.ResetAt.IsZero() {
			if exists && !rejected && (!next.UsageTrusted || next.UsedPercent == 0) {
				continue
			}
			if exists && (previous.ResetAt.IsZero() || previous.ResetAt.After(observedAt)) {
				next.ResetAt = previous.ResetAt
				if !previous.UsageTrusted || previous.UsedPercent > next.UsedPercent {
					next.UsageTrusted = previous.UsageTrusted
					next.UsedPercent = previous.UsedPercent
				}
			} else if next.UsedPercent == 0 && !rejected {
				next.UsageTrusted = false
			}
		}
		if rejected {
			next.UsageTrusted = false
		}
		if q.ReserveWindows == nil {
			q.ReserveWindows = make(map[string]quotaReserveWindow, 2)
		}
		q.ReserveWindows[name] = next
	}
	limitReached, _ := strconv.ParseBool(signal("X-Codex-Limit-Reached"))
	if provider == "codex" && limitReached {
		for name, window := range q.ReserveWindows {
			if window.ObservedAt.After(observedAt) {
				continue
			}
			window.UsageTrusted = false
			window.ObservedAt = observedAt
			if !window.ResetAt.After(observedAt) {
				window.ResetAt = time.Time{}
			}
			q.ReserveWindows[name] = window
		}
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
