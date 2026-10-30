package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeNativeWorkloadMarkerKeepsOutboundPrefixIdentical(t *testing.T) {
	userID, _ := json.Marshal(`{"device_id":"0000000000000000000000000000000000000000000000000000000000000001","account_uuid":"11111111-2222-4333-8444-555555555555","session_id":"11111111-2222-4333-8444-555555555556"}`)
	for _, test := range []struct {
		name   string
		stream bool
		ttl    string
	}{
		{name: "execute stream bare ttl", stream: true},
		{name: "execute stream one hour ttl", stream: true, ttl: `,"ttl":"1h"`},
		{name: "execute bare ttl"},
		{name: "execute one hour ttl", ttl: `,"ttl":"1h"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := `{"type":"ephemeral"` + test.ttl + `}`
			payload := []byte(fmt.Sprintf(`{"model":"claude-opus-5-5","max_tokens":1000,"tools":[{"name":"read_file","description":"Read a file.","input_schema":{"type":"object","properties":{"path":{"type":"string"}}},"cache_control":%[1]s}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.312; cc_entrypoint=sdk-ts; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.","cache_control":%[1]s},{"type":"text","text":"Stable project instructions.","cache_control":%[1]s}],"messages":[{"role":"user","content":[{"type":"text","text":"Stable task.","cache_control":%[1]s}]}],"metadata":{"user_id":%[2]s},"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},"stream":%[3]t}`, marker, userID, test.stream))
			auth := &cliproxyauth.Auth{
				ID:         "native-workload-fixture",
				Attributes: map[string]string{"api_key": "sk-ant-oat-test-oauth-key"},
				Metadata: map[string]any{
					"account_uuid": "11111111-2222-4333-8444-555555555555",
					claudeauth.ClaudeDeviceIDsMetadataKey: []string{
						"0000000000000000000000000000000000000000000000000000000000000001",
					},
				},
			}
			var bodies [][]byte
			var userAgents []string
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				bodies = append(bodies, body)
				userAgents = append(userAgents, req.Header.Get("User-Agent"))
				header := http.Header{"Content-Type": {"application/json"}}
				response := `{"id":"msg_fixture","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":1,"output_tokens":1}}`
				if test.stream {
					header.Set("Content-Type", "text/event-stream")
					response = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"model\":\"claude-opus-5-5\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				}
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			exec := NewClaudeExecutor(&config.Config{})

			inboundUserAgents := []string{
				"claude-cli/2.1.312 (external, sdk-ts, agent-sdk/0.3.276)",
				"claude-cli/2.1.312 (external, sdk-ts, agent-sdk/0.3.276, workload/cron)",
				"claude-cli/2.1.312 (external, sdk-ts, agent-sdk/0.3.276)",
			}
			for _, userAgent := range inboundUserAgents {
				headers := http.Header{
					"User-Agent":     {userAgent},
					"X-App":          {"cli"},
					"Anthropic-Beta": {"claude-code-20250219,interleaved-thinking-2025-05-14"},
				}
				req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: test.stream, Headers: headers, OriginalRequest: payload}
				if test.stream {
					result, errStream := exec.ExecuteStream(ctx, auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := exec.Execute(ctx, auth, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
			}

			if len(bodies) != len(inboundUserAgents) {
				t.Fatalf("captured %d upstream requests, want %d", len(bodies), len(inboundUserAgents))
			}
			for i, body := range bodies {
				if got, want := gjson.GetBytes(body, "system").Raw, gjson.GetBytes(bodies[0], "system").Raw; got != want {
					t.Errorf("request %d (%s) outbound system = %s, want ordinary %s", i, inboundUserAgents[i], got, want)
				}
				for _, field := range []string{"system.#.cache_control", "messages", "tools"} {
					if got, want := gjson.GetBytes(body, field).Raw, gjson.GetBytes(payload, field).Raw; got != want {
						t.Errorf("request %d (%s) outbound %s = %s, want inbound %s", i, inboundUserAgents[i], field, got, want)
					}
				}
				if userAgents[i] != inboundUserAgents[i] {
					t.Errorf("request %d outbound User-Agent = %q, want caller %q", i, userAgents[i], inboundUserAgents[i])
				}
			}
		})
	}
}
