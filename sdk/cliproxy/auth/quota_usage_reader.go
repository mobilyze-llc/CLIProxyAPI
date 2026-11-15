package auth

import (
	"context"
	"net/http"
)

// QuotaUsage is the result of one usage-endpoint read. Headers holds the quota observation
// headers mapped from the response; nil means the response held no usable window.
// CredentialQuotaAvailable is true only when the response proves that no window able to
// impose a credential-wide limit is exhausted: every such window is present, each is either
// explicitly idle or below its limit with a reset after the read started, and at least one
// window is active.
type QuotaUsage struct {
	Headers                  http.Header
	CredentialQuotaAvailable bool
}

// QuotaUsageReader is an optional ProviderExecutor capability. ReadQuotaUsage reads the
// credential's subscription usage endpoint with the identity of the provider's CLI. A non-2xx
// response is an error that carries the status. The caller decides which credentials are
// eligible, by provider as well as capability: an executor that embeds a reader, such as
// KimiExecutor embedding ClaudeExecutor, inherits it.
type QuotaUsageReader interface {
	ReadQuotaUsage(ctx context.Context, auth *Auth) (QuotaUsage, error)
}
