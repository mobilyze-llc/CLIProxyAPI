package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
	resp, errFetch := auth.FetchOAuthUsage(ctx, "tok-usage", "claude-cli/2.1.288 (external, cli)")
	if errFetch != nil {
		t.Fatalf("FetchOAuthUsage() error = %v", errFetch)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
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

// TestFetchOAuthUsageClientHelloMatchesControlPlane dials the usage read and the
// profile lookup through the production control-plane transport and compares their
// ClientHello records with the per-connection random values masked.
func TestFetchOAuthUsageClientHelloMatchesControlPlane(t *testing.T) {
	usage := captureControlPlaneClientHello(t, func(ctx context.Context, auth *ClaudeAuth) error {
		_, errFetch := auth.FetchOAuthUsage(ctx, "tok", "claude-cli/2.1.288 (external, cli)")
		return errFetch
	})
	profile := captureControlPlaneClientHello(t, func(ctx context.Context, auth *ClaudeAuth) error {
		_, errFetch := auth.FetchOAuthProfile(ctx, "tok")
		return errFetch
	})
	if !bytes.Equal(maskClientHelloRandomness(t, usage), maskClientHelloRandomness(t, profile)) {
		t.Fatalf("usage ClientHello differs from the control-plane ClientHello\nusage:   %x\nprofile: %x", usage, profile)
	}
}

func captureControlPlaneClientHello(t *testing.T, fetch func(context.Context, *ClaudeAuth) error) []byte {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := clientConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close client connection: %v", errClose)
		}
	})
	transport := newUtlsRoundTripper(nil)
	transport.dialer = claudeTestDialer{conn: clientConn}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fetch(ctx, &ClaudeAuth{httpClient: &http.Client{Transport: transport}}) }()

	if errDeadline := serverConn.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
		t.Fatal(errDeadline)
	}
	header := make([]byte, 5)
	if _, errRead := io.ReadFull(serverConn, header); errRead != nil {
		t.Fatal(errRead)
	}
	payload := make([]byte, int(binary.BigEndian.Uint16(header[3:5])))
	if _, errRead := io.ReadFull(serverConn, payload); errRead != nil {
		t.Fatal(errRead)
	}
	if errClose := serverConn.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	select {
	case errFetch := <-done:
		if errFetch == nil {
			t.Fatal("fetch succeeded although the server closed during the handshake")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not exit after the server closed")
	}
	return append(header, payload...)
}

// maskClientHelloRandomness zeroes the client random, the legacy session ID and the
// key share, which differ on every connection, and keeps every other byte.
func maskClientHelloRandomness(t *testing.T, record []byte) []byte {
	t.Helper()
	masked := append([]byte(nil), record...)
	fail := func() { t.Fatalf("malformed ClientHello record %x", record) }
	// Record header (5), handshake header (4), legacy version (2), then random (32).
	offset := 5 + 4 + 2
	if len(masked) < offset+33 {
		fail()
	}
	clear(masked[offset : offset+32])
	offset += 32
	sessionIDLength := int(masked[offset])
	offset++
	if len(masked) < offset+sessionIDLength+2 {
		fail()
	}
	clear(masked[offset : offset+sessionIDLength])
	offset += sessionIDLength
	offset += 2 + int(binary.BigEndian.Uint16(masked[offset:]))
	if len(masked) < offset+1 {
		fail()
	}
	offset += 1 + int(masked[offset])
	if len(masked) < offset+2 {
		fail()
	}
	offset += 2
	for offset+4 <= len(masked) {
		extensionType := binary.BigEndian.Uint16(masked[offset:])
		extensionLength := int(binary.BigEndian.Uint16(masked[offset+2:]))
		offset += 4
		if offset+extensionLength > len(masked) {
			fail()
		}
		if extensionType == 51 {
			// key_share: client_shares length (2), group (2), key length (2), key.
			clear(masked[offset+6 : offset+extensionLength])
		}
		offset += extensionLength
	}
	if offset != len(masked) {
		fail()
	}
	return masked
}
