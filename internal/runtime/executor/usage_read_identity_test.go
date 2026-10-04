package executor

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type usageReadCapture struct {
	method string
	url    string
	header http.Header
}

type quotaUsageReadFunc func(context.Context, *cliproxyauth.Auth) (http.Header, error)

// captureUsageRead runs one usage read and returns what the transport received. The
// injected round tripper answers every request, so the read cannot reach a real endpoint.
func captureUsageRead(t *testing.T, read quotaUsageReadFunc, auth *cliproxyauth.Auth) usageReadCapture {
	t.Helper()
	var captured []usageReadCapture
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		captured = append(captured, usageReadCapture{method: req.Method, url: req.URL.String(), header: req.Header.Clone()})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
	}))
	if _, errRead := read(ctx, auth); errRead != nil {
		t.Fatalf("usage read: %v", errRead)
	}
	if len(captured) != 1 {
		t.Fatalf("transport received %d requests, want 1", len(captured))
	}
	return captured[0]
}

func usageReadOAuthAuth(provider string, attrs map[string]string) *cliproxyauth.Auth {
	// No refresh token and a far expiry, so nothing can attempt a refresh.
	return &cliproxyauth.Auth{ID: provider + "-usage", Provider: provider, Attributes: attrs, Metadata: map[string]any{
		"type": provider, "access_token": "tok-" + provider, "account_id": "account-1",
		"expired": time.Now().Add(240 * time.Hour).Format(time.RFC3339),
	}}
}

// TestUsageReadTransportReceivesCLIIdentity compares the full request each provider's
// usage read hands its transport against the native CLI header set, written out here
// rather than derived from the helpers under test.
func TestUsageReadTransportReceivesCLIIdentity(t *testing.T) {
	cfg := &config.Config{}
	cfg.ClaudeHeaderDefaults.UserAgent = "claude-cli/2.1.288 (external, cli)"

	claude := captureUsageRead(t, NewClaudeExecutor(cfg).ReadQuotaUsage, usageReadOAuthAuth("claude", nil))
	wantClaude := http.Header{
		"Accept":          {"application/json, text/plain, */*"},
		"Content-Type":    {"application/json"},
		"User-Agent":      {"claude-cli/2.1.288 (external, cli)"},
		"Authorization":   {"Bearer tok-claude"},
		"Anthropic-Beta":  {"oauth-2025-04-20"},
		"Accept-Encoding": {"gzip, compress, deflate, br"},
		"Connection":      {"close"},
	}
	if claude.method != http.MethodGet || claude.url != "https://api.anthropic.com/api/oauth/usage" {
		t.Errorf("Claude usage read = %s %s", claude.method, claude.url)
	}
	if !reflect.DeepEqual(claude.header, wantClaude) {
		t.Errorf("Claude usage read headers =\n%v\nwant\n%v", claude.header, wantClaude)
	}

	codex := captureUsageRead(t, NewCodexAutoExecutor(cfg).ReadQuotaUsage, usageReadOAuthAuth("codex", nil))
	wantCodex := http.Header{
		"Authorization":      {"Bearer tok-codex"},
		"User-Agent":         {"codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"},
		"Originator":         {"codex-tui"},
		"Chatgpt-Account-Id": {"account-1"},
	}
	if codex.method != http.MethodGet || codex.url != "https://chatgpt.com/backend-api/wham/usage" {
		t.Errorf("Codex usage read = %s %s", codex.method, codex.url)
	}
	if !reflect.DeepEqual(codex.header, wantCodex) {
		t.Errorf("Codex usage read headers =\n%v\nwant\n%v", codex.header, wantCodex)
	}
}

// TestCodexUsageReadMatchesInferenceIdentity checks the User-Agent and account the
// usage read sends against literal expectations, next to what HTTP inference sends for
// the same credential and configuration when the client sends its own User-Agent.
func TestCodexUsageReadMatchesInferenceIdentity(t *testing.T) {
	const (
		customUA     = "custom-agent/1.0"
		configuredUA = "codex-configured/2.0"
		clientUA     = "client-agent/3.0"
	)
	cloakingOff := &config.Config{}
	cloakingOff.Codex.DisableCodexCloaking = true
	cloakingOffConfiguredUA := &config.Config{}
	cloakingOffConfiguredUA.Codex.DisableCodexCloaking = true
	cloakingOffConfiguredUA.CodexHeaderDefaults.UserAgent = configuredUA

	cases := []struct {
		name            string
		cfg             *config.Config
		attrs           map[string]string
		accountID       string
		wantUsageUA     string
		wantInferenceUA string
	}{
		{name: "cloaking enabled", cfg: &config.Config{}, accountID: "account-1",
			wantUsageUA: codexUserAgent, wantInferenceUA: codexUserAgent},
		{name: "cloaking enabled overrides credential User-Agent", cfg: &config.Config{}, accountID: "account-1",
			attrs: map[string]string{"header:User-Agent": customUA}, wantUsageUA: codexUserAgent, wantInferenceUA: codexUserAgent},
		{name: "cloaking disabled with credential User-Agent", cfg: cloakingOff, accountID: "account-1",
			attrs: map[string]string{"header:User-Agent": customUA}, wantUsageUA: customUA, wantInferenceUA: customUA},
		{name: "cloaking disabled with configured User-Agent", cfg: cloakingOffConfiguredUA, accountID: "account-1",
			wantUsageUA: configuredUA, wantInferenceUA: configuredUA},
		{name: "cloaking disabled without configured User-Agent", cfg: cloakingOff, accountID: "account-1",
			wantUsageUA: codexUserAgent, wantInferenceUA: clientUA},
		{name: "empty account id", cfg: &config.Config{}, accountID: "",
			wantUsageUA: codexUserAgent, wantInferenceUA: codexUserAgent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := usageReadOAuthAuth("codex", tc.attrs)
			auth.Metadata["account_id"] = tc.accountID
			usage := captureUsageRead(t, NewCodexExecutor(tc.cfg).ReadQuotaUsage, auth)

			inference, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			applyCodexHeaders(inference, auth, "tok-codex", true, tc.cfg, http.Header{"User-Agent": {clientUA}})

			for _, sent := range []struct {
				name   string
				header http.Header
				wantUA string
			}{{"usage read", usage.header, tc.wantUsageUA}, {"inference", inference.Header, tc.wantInferenceUA}} {
				if got := sent.header.Get("User-Agent"); got != sent.wantUA {
					t.Errorf("%s User-Agent = %q, want %q", sent.name, got, sent.wantUA)
				}
				if got := sent.header.Get("Authorization"); got != "Bearer tok-codex" {
					t.Errorf("%s Authorization = %q", sent.name, got)
				}
				if got := sent.header.Get("Originator"); got != codexOriginator {
					t.Errorf("%s Originator = %q, want %q", sent.name, got, codexOriginator)
				}
				got, present := sent.header["Chatgpt-Account-Id"]
				if tc.accountID == "" && present {
					t.Errorf("%s carries Chatgpt-Account-Id %q for an empty account id", sent.name, got)
				}
				if tc.accountID != "" && sent.header.Get("Chatgpt-Account-Id") != tc.accountID {
					t.Errorf("%s Chatgpt-Account-Id = %q, want %q", sent.name, got, tc.accountID)
				}
			}
			for _, name := range []string{"Accept", "Session-Id", "Version", "X-Codex-Beta-Features", "Content-Type", "Connection"} {
				if got := usage.header.Get(name); got != "" {
					t.Errorf("usage read carries inference-only header %s = %q", name, got)
				}
			}
		})
	}
}

// TestClaudeUsageReadDecodesCompressedResponse checks that a gzip-compressed usage
// response, which the advertised Accept-Encoding invites, still parses.
func TestClaudeUsageReadDecodesCompressedResponse(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, errWrite := writer.Write([]byte(`{"five_hour":{"utilization":14.0,"resets_at":"2026-10-04T02:40:00+00:00"},"seven_day":{"utilization":31.0,"resets_at":"2026-10-08T23:00:00+00:00"}}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Request: req,
			Header: http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}},
			Body:   io.NopCloser(bytes.NewReader(compressed.Bytes()))}, nil
	}))
	headers, errRead := NewClaudeExecutor(&config.Config{}).ReadQuotaUsage(ctx, usageReadOAuthAuth("claude", nil))
	if errRead != nil {
		t.Fatalf("usage read: %v", errRead)
	}
	if got := headers.Get("Anthropic-Ratelimit-Unified-7d-Utilization"); got != "0.31" {
		t.Fatalf("parsed 7d utilization = %q, want 0.31 (headers %v)", got, headers)
	}
}

// TestClaudeUsageReadDialsControlPlaneTLSThroughCredentialProxy sends the usage read
// through the production transport, with the credential's proxy set to a local CONNECT
// listener and no context round tripper. The first TLS record in the tunnel must carry
// no ALPN extension (type 16): the OAuth control-plane ClientHello has none, while the
// inference ClientHello advertises http/1.1.
func TestClaudeUsageReadDialsControlPlaneTLSThroughCredentialProxy(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	defer func() { _ = listener.Close() }()

	type tunnel struct {
		target string
		record []byte
		err    error
	}
	tunnels := make(chan tunnel, 1)
	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			tunnels <- tunnel{err: errAccept}
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		connect, errConnect := http.ReadRequest(reader)
		if errConnect != nil {
			tunnels <- tunnel{err: errConnect}
			return
		}
		if _, errWrite := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
			tunnels <- tunnel{err: errWrite}
			return
		}
		header := make([]byte, 5)
		if _, errRead := io.ReadFull(reader, header); errRead != nil {
			tunnels <- tunnel{err: errRead}
			return
		}
		payload := make([]byte, binary.BigEndian.Uint16(header[3:5]))
		if _, errRead := io.ReadFull(reader, payload); errRead != nil {
			tunnels <- tunnel{err: errRead}
			return
		}
		tunnels <- tunnel{target: connect.Method + " " + connect.Host, record: append(header, payload...)}
	}()

	auth := usageReadOAuthAuth("claude", nil)
	auth.ProxyURL = "http://" + listener.Addr().String()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, errRead := NewClaudeExecutor(&config.Config{}).ReadQuotaUsage(ctx, auth); errRead == nil {
		t.Fatal("usage read succeeded although the tunnel closed during the handshake")
	}
	got := <-tunnels
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.target != "CONNECT api.anthropic.com:443" {
		t.Fatalf("proxy received %q, want CONNECT api.anthropic.com:443", got.target)
	}
	extensions := clientHelloExtensionTypes(t, got.record)
	if !slices.Contains(extensions, 0) {
		t.Fatalf("ClientHello extensions %v carry no server_name; the record is not a ClientHello", extensions)
	}
	if slices.Contains(extensions, 16) {
		t.Fatalf("ClientHello extensions %v advertise ALPN, which only the inference profile sends", extensions)
	}
}

// clientHelloExtensionTypes lists the extension types of a TLS record holding one ClientHello.
func clientHelloExtensionTypes(t *testing.T, record []byte) []uint16 {
	t.Helper()
	fail := func() { t.Fatalf("malformed ClientHello record %x", record) }
	if len(record) < 5 || record[0] != 22 {
		fail()
	}
	// Record header (5), handshake header (4), legacy version (2), random (32).
	offset := 5 + 4 + 2 + 32
	skip := func(lengthBytes int) {
		if offset+lengthBytes > len(record) {
			fail()
		}
		length := 0
		for _, b := range record[offset : offset+lengthBytes] {
			length = length<<8 | int(b)
		}
		offset += lengthBytes + length
	}
	skip(1) // legacy session ID
	skip(2) // cipher suites
	skip(1) // compression methods
	if offset+2 > len(record) {
		fail()
	}
	end := offset + 2 + int(binary.BigEndian.Uint16(record[offset:]))
	offset += 2
	if end != len(record) {
		fail()
	}
	var types []uint16
	for offset < end {
		if offset+4 > end {
			fail()
		}
		types = append(types, binary.BigEndian.Uint16(record[offset:]))
		offset += 2
		skip(2)
	}
	if offset != end {
		fail()
	}
	return types
}
