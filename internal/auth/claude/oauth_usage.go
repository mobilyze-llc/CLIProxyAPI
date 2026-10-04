package claude

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	// UsageURL is the subscription usage endpoint Claude Code reads through its
	// Axios API client.
	UsageURL             = "https://api.anthropic.com" + claudeOAuthUsagePath
	claudeOAuthUsagePath = "/api/oauth/usage"
)

// NewClaudeAuthWithRoundTripper creates a ClaudeAuth whose requests go through rt
// instead of the control-plane uTLS transport.
func NewClaudeAuthWithRoundTripper(rt http.RoundTripper) *ClaudeAuth {
	return &ClaudeAuth{httpClient: &http.Client{Transport: rt}}
}

// FetchOAuthUsage reads the account's subscription usage with the request shape
// Claude Code's API client sends: the Axios control-plane headers with the CLI
// User-Agent and the OAuth beta. The returned response carries the decoded body,
// because the request advertises compression; the caller closes it.
func (o *ClaudeAuth) FetchOAuthUsage(ctx context.Context, accessToken, userAgent string) (*http.Response, error) {
	if o == nil || o.httpClient == nil {
		return nil, fmt.Errorf("fetch Claude OAuth usage: HTTP client is nil")
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("fetch Claude OAuth usage: access token is empty")
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, UsageURL, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("create Claude OAuth usage request: %w", errRequest)
	}
	applyClaudeOAuthAxiosHeaders(req)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("fetch Claude OAuth usage: %w", errDo)
	}
	body, errRead := readClaudeOAuthResponseBody(resp)
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("failed to close Claude OAuth usage response body: %v", errClose)
	}
	if errRead != nil {
		return nil, fmt.Errorf("read Claude OAuth usage response: %w", errRead)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.ContentLength = int64(len(body))
	resp.Uncompressed = true
	return resp, nil
}
