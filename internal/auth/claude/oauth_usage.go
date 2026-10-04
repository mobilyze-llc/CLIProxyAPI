package claude

import (
	"context"
	"net/http"
)

const (
	// claudeOAuthUsageURL is the subscription usage endpoint Claude Code reads through
	// its Axios API client.
	claudeOAuthUsageURL  = "https://api.anthropic.com" + claudeOAuthUsagePath
	claudeOAuthUsagePath = "/api/oauth/usage"
)

// claudeOAuthUsageHeaderOrder is the order Claude Code's Axios API client emits
// for the subscription usage read, which carries the CLI User-Agent and the
// OAuth beta instead of the profile lookup's Cache-Control.
var claudeOAuthUsageHeaderOrder = []string{
	"Accept",
	"Content-Type",
	"User-Agent",
	"Authorization",
	"anthropic-beta",
	"Accept-Encoding",
	"Host",
	"Connection",
}

// NewClaudeAuthWithRoundTripper creates a ClaudeAuth whose requests go through rt
// instead of the control-plane uTLS transport.
func NewClaudeAuthWithRoundTripper(rt http.RoundTripper) *ClaudeAuth {
	return &ClaudeAuth{httpClient: &http.Client{Transport: rt}}
}

// FetchOAuthUsage reads the account's subscription usage with the request shape
// Claude Code's API client sends: the Axios control-plane headers with the CLI
// User-Agent and the OAuth beta, and no Cache-Control. It returns the decoded body.
func (o *ClaudeAuth) FetchOAuthUsage(ctx context.Context, accessToken, userAgent string) ([]byte, error) {
	return o.fetchOAuthControlPlaneJSON(ctx, claudeOAuthUsageURL, accessToken, "usage", func(header http.Header) {
		header.Del("Cache-Control")
		header.Set("User-Agent", userAgent)
		header.Set("anthropic-beta", "oauth-2025-04-20")
	})
}
