package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// codexUsageURL is the ChatGPT backend usage endpoint the Codex CLI reads.
const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

var (
	_ cliproxyauth.QuotaUsageReader = (*CodexExecutor)(nil)
	_ cliproxyauth.QuotaUsageReader = (*CodexAutoExecutor)(nil)
)

// ReadQuotaUsage reads the credential's ChatGPT usage with the credential and client
// identity HTTP inference sends for the same credential.
func (e *CodexExecutor) ReadQuotaUsage(ctx context.Context, auth *cliproxyauth.Auth) (http.Header, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, codexUsageURL, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("create codex usage request: %w", errRequest)
	}
	accessToken, _ := codexCreds(auth)
	applyCodexIdentityHeaders(req, auth, accessToken, e.cfg, nil)
	resp, errDo := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0).Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("fetch codex usage: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("failed to close codex usage response body: %v", errClose)
		}
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("fetch codex usage failed with status %d", resp.StatusCode)
	}
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("read codex usage response: %w", errRead)
	}
	return helps.ParseCodexUsageHeaders(body), nil
}

// ReadQuotaUsage reads usage through the HTTP executor.
func (e *CodexAutoExecutor) ReadQuotaUsage(ctx context.Context, auth *cliproxyauth.Auth) (http.Header, error) {
	if e == nil || e.httpExec == nil {
		return nil, fmt.Errorf("codex auto executor: http executor is nil")
	}
	return e.httpExec.ReadQuotaUsage(ctx, auth)
}
