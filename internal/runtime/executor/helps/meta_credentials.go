package helps

import (
	"strings"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// MetaCreds resolves the Meta API base URL and the non-DCA bearer token for a Meta auth.
func MetaCreds(a *cliproxyauth.Auth) (baseURL, token string) {
	baseURL = metaauth.DefaultAPIBaseURL
	if a == nil {
		return baseURL, ""
	}

	if a.Attributes != nil {
		if b := strings.TrimSpace(a.Attributes["base_url"]); b != "" {
			baseURL = b
		}
		if k := strings.TrimSpace(a.Attributes["api_key"]); k != "" && !strings.HasPrefix(k, "dca:") {
			token = k
		} else if t := strings.TrimSpace(a.Attributes["access_token"]); t != "" && !strings.HasPrefix(t, "dca:") {
			token = t
		}
	}
	if a.Metadata != nil {
		if baseURL == metaauth.DefaultAPIBaseURL {
			if b, ok := a.Metadata["base_url"].(string); ok && strings.TrimSpace(b) != "" {
				baseURL = strings.TrimSpace(b)
			} else if b, ok := a.Metadata["api_base_url"].(string); ok && strings.TrimSpace(b) != "" {
				baseURL = strings.TrimSpace(b)
			}
		}
		if token == "" {
			if k, ok := a.Metadata["api_key"].(string); ok && strings.TrimSpace(k) != "" && !strings.HasPrefix(strings.TrimSpace(k), "dca:") {
				token = strings.TrimSpace(k)
			} else if t, ok := a.Metadata["access_token"].(string); ok && strings.TrimSpace(t) != "" && !strings.HasPrefix(strings.TrimSpace(t), "dca:") {
				token = strings.TrimSpace(t)
			}
		}
	}
	if token == "" && a.Storage != nil {
		if ms, ok := a.Storage.(*metaauth.MetaTokenStorage); ok && ms != nil {
			if ms.APIKey != "" {
				token = ms.APIKey
			} else if ms.AccessToken != "" && !strings.HasPrefix(ms.AccessToken, "dca:") {
				token = ms.AccessToken
			}
			if ms.BaseURL != "" && baseURL == metaauth.DefaultAPIBaseURL {
				baseURL = ms.BaseURL
			}
		}
	}
	return baseURL, token
}
