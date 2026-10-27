package executor

import (
	"context"
	"net/http"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var _ cliproxyauth.QuotaUsageReader = (*ClaudeExecutor)(nil)

// ReadQuotaUsage reads the credential's subscription usage the way Claude Code does:
// through the OAuth control-plane transport with the configured Claude Code User-Agent.
func (e *ClaudeExecutor) ReadQuotaUsage(ctx context.Context, auth *cliproxyauth.Auth) (http.Header, error) {
	accessToken, _ := claudeCreds(auth)
	body, errFetch := e.claudeOAuthUsageService(ctx, auth).FetchOAuthUsage(ctx, accessToken, helps.ClaudeUsageUserAgent(e.cfg))
	if errFetch != nil {
		return nil, errFetch
	}
	return helps.ParseClaudeUsageHeaders(body), nil
}

// claudeOAuthUsageService resolves the proxy the way helps.NewUtlsHTTPClient does: the
// request-scoped override, then the credential proxy, then the global proxy. A round
// tripper injected through the context replaces the transport only when no proxy applies.
func (e *ClaudeExecutor) claudeOAuthUsageService(ctx context.Context, auth *cliproxyauth.Auth) *claudeauth.ClaudeAuth {
	proxyURL := helps.EffectiveProxyURL(ctx, e.cfg, auth)
	if proxyURL == "" {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			return claudeauth.NewClaudeAuthWithRoundTripper(rt)
		}
	}
	return claudeauth.NewClaudeAuthWithProxyURL(e.cfg, proxyURL)
}
