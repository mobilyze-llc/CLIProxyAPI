package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeExecutorT3CodePreservesNativeWire(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "count"} {
		for _, scheduled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/scheduled=%t", mode, scheduled), func(t *testing.T) {
				helps.ResetClaudeDeviceProfileCache()
				var body []byte
				var headers http.Header
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ = io.ReadAll(r.Body)
					headers = r.Header.Clone()
					if mode == "count" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"input_tokens":42}`)
					} else if mode == "stream" {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_t3\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"msg_t3","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
					}
				}))
				defer server.Close()
				suffix := ""
				if scheduled {
					suffix = ", workload/cron"
				}
				userAgent := "claude-cli/2.1.316 (external, t3-code, agent-sdk/0.3.276" + suffix + ")"
				incoming := claudeNativeHelperHeaders("claude-code-20250219,"+claudeNativeHelperCoreBetas+",extended-cache-ttl-2025-04-11", "gzip, deflate, br, zstd")
				incoming.Set("User-Agent", userAgent)
				payload := []byte(`{"model":"claude-sonnet-5-5","max_tokens":1024,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.316; cc_entrypoint=t3-code;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"Caller instructions","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"metadata":{"user_id":` + fmt.Sprintf("%q", claudeNativeHelperUserID) + `}}`)
				executor := NewClaudeExecutor(&config.Config{})
				request := cliproxyexecutor.Request{Model: "claude-sonnet-5-5", Payload: payload}
				options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Headers: incoming}
				auth := claudeNativeHelperOAuthAuth(server.URL)
				switch mode {
				case "execute":
					if _, errExecute := executor.Execute(context.Background(), auth, request, options); errExecute != nil {
						t.Fatal(errExecute)
					}
				case "stream":
					result, errStream := executor.ExecuteStream(context.Background(), auth, request, options)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				case "count":
					if _, errCount := executor.countTokensUpstream(context.Background(), auth, request, options); errCount != nil {
						t.Fatal(errCount)
					}
				}
				if got := headers.Get("User-Agent"); got != userAgent {
					t.Fatalf("User-Agent = %q, want %q", got, userAgent)
				}
				if mode == "count" {
					if got := gjson.GetBytes(body, "system.#").Int(); got != 2 {
						t.Fatalf("count system blocks = %d, want 2 (billing excluded)", got)
					}
					for i := 0; i < 2; i++ {
						if got, want := gjson.GetBytes(body, fmt.Sprintf("system.%d", i)).Raw, gjson.GetBytes(payload, fmt.Sprintf("system.%d", i+1)).Raw; got != want {
							t.Fatalf("count system block changed: got %s, want %s", got, want)
						}
					}
				} else {
					if got := gjson.GetBytes(body, "system.0.text").String(); !strings.Contains(got, "cc_entrypoint=t3-code;") {
						t.Fatalf("billing attribution changed: %q", got)
					}
					if got := gjson.GetBytes(body, "system.#").Int(); got != 3 {
						t.Fatalf("system blocks = %d, want 3", got)
					}
					for _, path := range []string{"system.1", "system.2"} {
						if got, want := gjson.GetBytes(body, path).Raw, gjson.GetBytes(payload, path).Raw; got != want {
							t.Fatalf("%s changed: got %s, want %s", path, got, want)
						}
					}
				}
				for _, path := range []string{"tools", "messages"} {
					if got, want := gjson.GetBytes(body, path).Raw, gjson.GetBytes(payload, path).Raw; got != want {
						t.Fatalf("%s changed: got %s, want %s", path, got, want)
					}
				}
			})
		}
	}
}
