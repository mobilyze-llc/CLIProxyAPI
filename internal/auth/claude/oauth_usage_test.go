package claude

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
)

// TestFetchOAuthUsageSerializesNativeHeaderOrder checks the bytes the usage read
// writes: header names, casing and order as Claude Code's API client sends them.
// The connection is the ordered serializer the control-plane dial installs, over a
// pipe without TLS so the request bytes are readable.
func TestFetchOAuthUsageSerializesNativeHeaderOrder(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: &http.Transport{
		DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
			return httpwire.NewOrderedRequestConn(clientConn, claudeOAuthRequestHeaderOrder), nil
		},
	}}}

	lines := make(chan []string, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		var got []string
		for {
			line, errRead := reader.ReadString('\n')
			if errRead != nil {
				break
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			got = append(got, line)
		}
		lines <- got
		body := `{"five_hour":{"utilization":14.0,"resets_at":"2026-10-04T02:40:00+00:00"}}`
		_, _ = fmt.Fprintf(serverConn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		_ = serverConn.Close()
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, errFetch := auth.FetchOAuthUsage(ctx, "tok-usage", "claude-cli/2.1.288 (external, cli)"); errFetch != nil {
		t.Fatalf("FetchOAuthUsage() error = %v", errFetch)
	}
	want := []string{
		"GET /api/oauth/usage HTTP/1.1",
		"Accept: application/json, text/plain, */*",
		"Content-Type: application/json",
		"User-Agent: claude-cli/2.1.288 (external, cli)",
		"Authorization: Bearer tok-usage",
		"anthropic-beta: oauth-2025-04-20",
		"Accept-Encoding: gzip, compress, deflate, br",
		"Host: api.anthropic.com",
		"Connection: close",
	}
	if got := <-lines; !reflect.DeepEqual(got, want) {
		t.Fatalf("usage request header lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
