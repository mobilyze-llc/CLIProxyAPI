package cliproxy

import (
	"context"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	quotaUsageReadInterval = 5 * time.Minute
	// quotaUsageReadTimeout bounds one read: the executor HTTP client has no timeout, and
	// reads run serially, so one hung read would otherwise stall every later read.
	quotaUsageReadTimeout = 30 * time.Second
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

// readQuotaUsage reads one credential's usage endpoint and records the result. It decides
// eligibility: OAuth credentials with a valid access token whose executor implements
// coreauth.QuotaUsageReader. A failed or rejected read leaves the previous observation untouched.
func (s *Service) readQuotaUsage(ctx context.Context, authID string) {
	auth, ok := s.coreManager.GetByID(authID)
	if !ok || auth.AuthKind() != coreauth.AuthKindOAuth || !auth.HasValidAccessToken(time.Now()) {
		return
	}
	exec, ok := s.coreManager.Executor(auth.Provider)
	if !ok {
		return
	}
	reader, ok := exec.(coreauth.QuotaUsageReader)
	if !ok {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, quotaUsageReadTimeout)
	defer cancel()
	observed, errRead := reader.ReadQuotaUsage(readCtx, auth)
	if errRead != nil {
		log.Warnf("quota usage read failed for auth %s: %v", authID, errRead)
		return
	}
	if observed != nil {
		s.coreManager.ObserveQuotaHeaders(authID, observed)
	}
}
