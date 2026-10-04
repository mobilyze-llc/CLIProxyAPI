package auth

import (
	"context"
	"net/http"
)

// QuotaUsageReader is an optional ProviderExecutor capability. ReadQuotaUsage reads the
// credential's subscription usage endpoint with the identity of the provider's CLI and
// returns the quota observation headers mapped from the response. Nil headers mean the
// response held no usable window. A non-2xx response is an error that carries the status.
// The caller decides which credentials are eligible, by provider as well as capability:
// an executor that embeds a reader, such as KimiExecutor embedding ClaudeExecutor, inherits it.
type QuotaUsageReader interface {
	ReadQuotaUsage(ctx context.Context, auth *Auth) (http.Header, error)
}
