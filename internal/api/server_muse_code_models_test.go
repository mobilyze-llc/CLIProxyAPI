package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestMuseCodeModelsRoute(t *testing.T) {
	const upstreamBody = `{"object":"list","data":[{"id":"muse-spark","object":"model"}]}`

	for _, testCase := range []struct {
		name          string
		clientKey     string
		registerMeta  bool
		wantStatus    int
		wantBody      string
		wantUpstreamN int32
	}{
		{name: "proxies_catalog", clientKey: "test-key", registerMeta: true, wantStatus: http.StatusTeapot, wantBody: upstreamBody, wantUpstreamN: 1},
		{name: "no_meta_credential", clientKey: "test-key", wantStatus: http.StatusServiceUnavailable},
		{name: "missing_client_key", registerMeta: true, wantStatus: http.StatusUnauthorized},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				if r.URL.Path != museCodeModelsPath || r.URL.RawQuery != "" {
					t.Errorf("upstream URL = %s, want %s", r.URL.String(), museCodeModelsPath)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer meta-api-key" {
					t.Errorf("upstream Authorization = %q, want Meta bearer", got)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTeapot)
				_, _ = w.Write([]byte(upstreamBody))
			}))
			t.Cleanup(upstream.Close)

			server := newTestServer(t)
			if testCase.registerMeta {
				credential := &auth.Auth{
					ID:       "meta-muse-code-models",
					Provider: "meta",
					Status:   auth.StatusActive,
					Attributes: map[string]string{
						"api_key":  "meta-api-key",
						"base_url": upstream.URL + "/v1?x=1",
					},
				}
				if _, errRegister := server.handlers.AuthManager.Register(t.Context(), credential); errRegister != nil {
					t.Fatalf("register Meta credential: %v", errRegister)
				}
			}

			req := httptest.NewRequest(http.MethodGet, museCodeModelsPath, nil)
			if testCase.clientKey != "" {
				req.Header.Set("Authorization", "Bearer "+testCase.clientKey)
			}
			recorder := httptest.NewRecorder()
			server.engine.ServeHTTP(recorder, req)

			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if testCase.wantBody != "" && recorder.Body.String() != testCase.wantBody {
				t.Fatalf("body = %q, want %q", recorder.Body.String(), testCase.wantBody)
			}
			if got := upstreamCalls.Load(); got != testCase.wantUpstreamN {
				t.Fatalf("upstream calls = %d, want %d", got, testCase.wantUpstreamN)
			}
		})
	}
}
