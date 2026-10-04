package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	quotaUsageReadInterval = 5 * time.Minute
	// quotaUsageReadTimeout bounds one read: the executor HTTP client has no timeout, and
	// reads run serially, so one hung read would otherwise stall every later read.
	quotaUsageReadTimeout = 30 * time.Second
	codexUsageURL         = "https://chatgpt.com/backend-api/wham/usage"
	claudeUsageURL        = "https://api.anthropic.com/api/oauth/usage"
)

// startQuotaUsageReads reads the usage endpoint of every Codex and Claude OAuth credential
// now and then every quotaUsageReadInterval until ctx ends. The result feeds the quota
// observation that soonest-reset ranks on, so the selector sees quota consumed outside the
// proxy and credentials the proxy has not used yet.
func (s *Service) startQuotaUsageReads(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(quotaUsageReadInterval)
		defer ticker.Stop()
		for {
			for _, auth := range s.coreManager.List() {
				if ctx.Err() != nil {
					return
				}
				s.readQuotaUsage(ctx, auth.ID)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// readQuotaUsage reads one credential's usage endpoint and records the result. A failed or
// rejected read leaves the previous observation untouched.
func (s *Service) readQuotaUsage(ctx context.Context, authID string) {
	auth, ok := s.coreManager.GetByID(authID)
	if !ok || auth.AuthKind() != coreauth.AuthKindOAuth || !auth.HasValidAccessToken(time.Now()) {
		return
	}
	headers := make(http.Header)
	var usageURL string
	var parse func([]byte) http.Header
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "codex":
		usageURL, parse = codexUsageURL, helps.ParseCodexUsageHeaders
		if accountID, _ := auth.Metadata["account_id"].(string); accountID != "" {
			headers.Set("Chatgpt-Account-Id", accountID)
		}
	case "claude":
		usageURL, parse = claudeUsageURL, helps.ParseClaudeUsageHeaders
		headers.Set("Anthropic-Beta", "oauth-2025-04-20")
	default:
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, quotaUsageReadTimeout)
	defer cancel()
	req, errRequest := http.NewRequestWithContext(readCtx, http.MethodGet, usageURL, nil)
	if errRequest != nil {
		return
	}
	req.Header = headers
	resp, errDo := s.coreManager.HttpRequest(readCtx, auth, req)
	if errDo != nil {
		log.Warnf("quota usage read failed for auth %s: %v", authID, errDo)
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("quota usage read: close response body: %v", errClose)
		}
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Warnf("quota usage read for auth %s returned status %d", authID, resp.StatusCode)
		return
	}
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		log.Warnf("quota usage read failed for auth %s: %v", authID, errRead)
		return
	}
	if observed := parse(body); observed != nil {
		s.coreManager.ObserveQuotaHeaders(authID, observed)
	}
}
