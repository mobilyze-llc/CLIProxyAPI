package auth

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	// weeklyWindowMinutes is the length of the weekly quota window advertised by Codex.
	weeklyWindowMinutes = 7 * 24 * 60
	week                = 7 * 24 * time.Hour
	resetGroupTolerance = time.Hour
)

// unknownResetLast orders a credential after every credential with a known reset.
var unknownResetLast = time.Unix(1<<62, 0)

// SoonestResetSelector prefers the credential whose weekly quota window resets soonest, so
// quota that would otherwise expire unused at the reset is consumed first. For one shared
// weekly limit no other order wastes less quota.
//
// Windows come from the passive quota snapshot (Quota.Signals) captured from upstream
// response headers. A credential is skipped while one of its observed windows is exhausted
// and that window's reset is still ahead; when every candidate is skipped this way the
// ordering alone decides and upstream 429 handling takes over.
//
// An unknown Claude weekly reset sorts first: the window runs on a fixed per-account
// schedule, so one request learns it. A passed Claude reset rolls forward by whole weeks.
// An unknown or passed Codex weekly reset sorts last: that window is usually unstarted and
// starting it early can waste it. Unknown resets keep input order (ID order), so providers
// without quota signals behave like fill-first.
//
// When the soonest credential is not skipped and its weekly reset is known, new picks rotate
// in ID order through the credentials of its provider that are not skipped and whose known
// weekly reset is at most one hour after it. Use it by pointer: it holds the rotation cursor.
type SoonestResetSelector struct {
	mu      sync.Mutex
	cursors map[string]int
}

// Pick selects the available auth whose weekly quota window resets soonest.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	var picked *Auth
	var pickedGated bool
	var pickedReset time.Time
	for _, auth := range available {
		gated, resetAt := soonestResetRank(auth, now)
		if picked == nil || pickedGated && !gated || gated == pickedGated && resetAt.Before(pickedReset) {
			picked, pickedGated, pickedReset = auth, gated, resetAt
		}
	}
	if picked == nil || pickedGated || pickedReset.IsZero() || pickedReset.Equal(unknownResetLast) {
		return picked, nil
	}
	groupProvider := strings.ToLower(strings.TrimSpace(picked.Provider))
	var group []*Auth
	for _, auth := range available {
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), groupProvider) {
			continue
		}
		gated, resetAt := soonestResetRank(auth, now)
		if !gated && resetAt.Sub(pickedReset) <= resetGroupTolerance {
			group = append(group, auth)
		}
	}
	slices.SortFunc(group, func(a, b *Auth) int { return strings.Compare(a.ID, b.ID) })
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cursors == nil {
		s.cursors = make(map[string]int)
	}
	cursor := s.cursors[groupProvider]
	s.cursors[groupProvider] = cursor + 1
	return group[cursor%len(group)], nil
}

// soonestResetRank reports whether an observed window of auth is exhausted until a future
// reset, and the weekly reset that orders auth among the candidates.
func soonestResetRank(auth *Auth, now time.Time) (gated bool, weeklyReset time.Time) {
	claude := strings.EqualFold(strings.TrimSpace(auth.Provider), "claude")
	weeklyReset = unknownResetLast
	if claude {
		weeklyReset = time.Time{}
	}
	for _, window := range observedQuotaWindows(auth) {
		if window.resetAt.IsZero() {
			continue
		}
		if window.exhausted && window.resetAt.After(now) {
			gated = true
		}
		if !window.weekly {
			continue
		}
		switch {
		case window.resetAt.After(now):
			weeklyReset = window.resetAt
		case claude:
			weeklyReset = window.resetAt.Add(week * (now.Sub(window.resetAt)/week + 1))
		}
	}
	return gated, weeklyReset
}

// quotaWindow is one observed quota window of a credential.
type quotaWindow struct {
	observed    bool
	usageKnown  bool
	weekly      bool
	exhausted   bool
	usedPercent float64
	resetAt     time.Time
}

// observedQuotaWindows reads the Claude 5h and 7d windows, or the Codex primary and
// secondary windows classified by length, from the credential-wide quota snapshot.
func observedQuotaWindows(auth *Auth) []quotaWindow {
	signal := func(name string) string {
		return strings.TrimSpace(auth.Quota.Signals[http.CanonicalHeaderKey(name)])
	}
	var windows []quotaWindow
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		for _, name := range []string{"5h", "7d"} {
			prefix := "Anthropic-Ratelimit-Unified-" + name + "-"
			resetAt := parseQuotaResetAt(signal(prefix+"Reset"), "", time.Time{})

			rawUsage := signal(prefix + "Utilization")
			utilization, errUsage := strconv.ParseFloat(rawUsage, 64)
			rejected := strings.EqualFold(signal(prefix+"Status"), "rejected")
			windows = append(windows, quotaWindow{
				observed:   rawUsage != "" || signal(prefix+"Reset") != "" || signal(prefix+"Status") != "",
				usageKnown: errUsage == nil && utilization >= 0 && utilization <= 1,
				weekly:     name == "7d", exhausted: utilization >= 1 || rejected,
				usedPercent: utilization * 100, resetAt: resetAt,
			})
		}
	case "codex":
		// Codex Pro reports its weekly window as primary, so windows are keyed by length.
		mostUsed := 0.0
		for _, name := range []string{"Primary", "Secondary"} {
			prefix := "X-Codex-" + name + "-"
			resetAt := parseQuotaResetAt(signal(prefix+"Reset-At"), signal(prefix+"Reset-After-Seconds"), auth.Quota.ObservedAt)
			// A missing window length keeps the window; it only means the length is unknown.
			minutes, _ := strconv.ParseInt(signal(prefix+"Window-Minutes"), 10, 64)

			rawUsage := signal(prefix + "Used-Percent")
			used, errUsage := strconv.ParseFloat(rawUsage, 64)
			if !resetAt.IsZero() {
				mostUsed = max(mostUsed, used)
			}
			windows = append(windows, quotaWindow{
				observed:   rawUsage != "" || signal(prefix+"Reset-At") != "" || signal(prefix+"Reset-After-Seconds") != "",
				usageKnown: errUsage == nil && used >= 0 && used <= 100,
				weekly:     minutes >= weeklyWindowMinutes, usedPercent: used, resetAt: resetAt,
			})
		}
		// The credential-wide limit-reached flag names no window; it is the most used one.
		limitReached, _ := strconv.ParseBool(signal("X-Codex-Limit-Reached"))
		for i := range windows {
			used := windows[i].usedPercent
			windows[i].exhausted = used >= 100 || limitReached && used == mostUsed
		}
	}
	return windows
}

// parseQuotaResetAt resolves a reset time from an absolute unix timestamp or, failing
// that, from a relative seconds value anchored at the observation time.
func parseQuotaResetAt(resetAt, resetAfterSeconds string, observedAt time.Time) time.Time {
	if unix, errParse := strconv.ParseFloat(resetAt, 64); errParse == nil && unix > 0 {
		return time.Unix(int64(unix), 0)
	}
	if parsed, errParse := time.Parse(time.RFC3339, resetAt); errParse == nil {
		return parsed
	}
	if observedAt.IsZero() {
		return time.Time{}
	}
	if seconds, errParse := strconv.ParseFloat(resetAfterSeconds, 64); errParse == nil && seconds >= 0 {
		return observedAt.Add(time.Duration(seconds * float64(time.Second)))
	}
	return time.Time{}
}
