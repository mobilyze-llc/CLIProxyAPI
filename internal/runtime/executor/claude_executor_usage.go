package executor

import (
	"context"
	"net/http"
	"strings"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func isClaudeOAuthUsageRead(auth *cliproxyauth.Auth, req *http.Request) bool {
	return auth != nil && auth.AuthKind() == cliproxyauth.AuthKindOAuth &&
		req.Method == http.MethodGet && req.URL != nil && req.URL.String() == claudeauth.UsageURL
}

// readClaudeOAuthUsage sends the usage read the way Claude Code does: through the
// OAuth control-plane transport with the credential's CLI User-Agent. The caller's
// headers are replaced, so the read carries only the native header set.
func (e *ClaudeExecutor) readClaudeOAuthUsage(ctx context.Context, auth *cliproxyauth.Auth) (*http.Response, error) {
	apiKey, _ := claudeCreds(auth)
	return e.claudeOAuthUsageService(ctx, auth).FetchOAuthUsage(ctx, apiKey, helps.ClaudeUsageUserAgent(e.cfg))
}

// claudeOAuthUsageService selects the proxy the profile lookup uses. Like
// helps.NewUtlsHTTPClient, a round tripper injected through the context replaces
// the transport only when no proxy applies.
func (e *ClaudeExecutor) claudeOAuthUsageService(ctx context.Context, auth *cliproxyauth.Auth) *claudeauth.ClaudeAuth {
	if strings.TrimSpace(auth.ProxyURL) == "" && (e.cfg == nil || strings.TrimSpace(e.cfg.ProxyURL) == "") {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			return claudeauth.NewClaudeAuthWithRoundTripper(rt)
		}
	}
	return claudeauth.NewClaudeAuthWithProxyURL(e.cfg, auth.ProxyURL)
}
