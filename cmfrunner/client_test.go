package cmfrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
)

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()

	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("parse test server URL %q: %v", server.URL, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse test server port %q: %v", portText, err)
	}

	return &Client{
		modelPath: "/models/example.cmf",
		port:      port,
		done:      make(chan struct{}),
		client:    newHTTPClient(),
	}
}

func TestClientCompletionForwardsRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/completions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode completion request: %v", err)
		}
		if request.Model != "example.cmf" || request.Prompt != "A prompt" {
			t.Fatalf("unexpected completion request: %#v", request)
		}
		if request.Temperature == nil || *request.Temperature != 0.25 {
			t.Fatalf("temperature was not forwarded: %#v", request.Temperature)
		}
		if request.MaxTokens == nil || *request.MaxTokens != 12 {
			t.Fatalf("max_tokens was not forwarded: %#v", request.MaxTokens)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"text":" completed","finish_reason":"length"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer server.Close()

	opts := api.DefaultOptions()
	opts.Temperature = 0.25
	opts.NumPredict = 12
	var responses []llm.CompletionResponse
	err := testClient(t, server).Completion(context.Background(), llm.CompletionRequest{
		Prompt:  "A prompt",
		Options: &opts,
	}, func(response llm.CompletionResponse) {
		responses = append(responses, response)
	})
	if err != nil {
		t.Fatalf("Completion() error = %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("received %d completion responses, want 1", len(responses))
	}
	response := responses[0]
	if response.Content != " completed" || !response.Done || response.DoneReason != llm.DoneReasonLength || response.PromptEvalCount != 3 || response.EvalCount != 2 {
		t.Fatalf("unexpected completion response: %#v", response)
	}
}

func TestClientChatForwardsSSEWithCaseInsensitiveContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode chat request: %v", err)
		}
		if request.Model != "example.cmf" || !request.Stream || len(request.Messages) != 1 || request.Messages[0].Role != "user" || request.Messages[0].Content != "Hello" {
			t.Fatalf("unexpected chat request: %#v", request)
		}

		w.Header().Set("Content-Type", "Text/Event-Stream; charset=utf-8")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	var responses []llm.ChatResponse
	err := testClient(t, server).Chat(context.Background(), llm.ChatRequest{
		Messages: []api.Message{{Role: "user", Content: "Hello"}},
		Options:  ptr(api.DefaultOptions()),
	}, func(response llm.ChatResponse) {
		responses = append(responses, response)
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("received %d chat responses, want 2", len(responses))
	}
	if response := responses[0]; response.Message.Role != "assistant" || response.Message.Content != "Hi" || response.Done {
		t.Fatalf("unexpected content response: %#v", response)
	}
	if response := responses[1]; !response.Done || response.DoneReason != llm.DoneReasonStop || response.PromptEvalCount != 4 || response.EvalCount != 1 {
		t.Fatalf("unexpected final response: %#v", response)
	}
}

func TestClientChatJSONDecodesStringToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_weather","function":{"index":0,"name":"weather","arguments":"{\"city\":\"Istanbul\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`)
	}))
	defer server.Close()

	var responses []llm.ChatResponse
	err := testClient(t, server).Chat(context.Background(), llm.ChatRequest{
		Messages: []api.Message{{Role: "user", Content: "What is the weather?"}},
		Options:  ptr(api.DefaultOptions()),
	}, func(response llm.ChatResponse) {
		responses = append(responses, response)
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("received %d chat responses, want 1", len(responses))
	}
	response := responses[0]
	if !response.Done || response.DoneReason != llm.DoneReasonStop {
		t.Fatalf("unexpected final response: %#v", response)
	}
	if len(response.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one call", response.Message.ToolCalls)
	}
	call := response.Message.ToolCalls[0]
	if call.ID != "call_weather" || call.Function.Name != "weather" || call.Function.Index != 0 {
		t.Fatalf("tool call = %#v, want weather call", call)
	}
	if city, ok := call.Function.Arguments.Get("city"); !ok || city != "Istanbul" {
		t.Fatalf("tool call arguments = %#v, want city Istanbul", call.Function.Arguments.ToMap())
	}
}

func TestClientPingChecksHealthStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("unexpected health path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer server.Close()

	if err := testClient(t, server).Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

func TestClientRejectsRedirects(t *testing.T) {
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected = true
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	err := testClient(t, source).Completion(context.Background(), llm.CompletionRequest{Prompt: "must not leave loopback"}, func(llm.CompletionResponse) {})
	if !errors.Is(err, errUnexpectedRedirect) {
		t.Fatalf("Completion() error = %v, want redirect rejection", err)
	}
	if redirected {
		t.Fatal("CMF client followed a redirect")
	}
}

func TestClientRefusesRequestsAfterOwnedRunnerExit(t *testing.T) {
	var requests atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer server.Close()

	client := testClient(t, server)
	client.doneErr = errors.New("sidecar exited")
	close(client.done)
	if err := client.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "runner exited") {
		t.Fatalf("Ping() error = %v, want exited-runner error", err)
	}
	if requests.Load() {
		t.Fatal("Ping contacted a loopback process after the owned runner exited")
	}
}

func TestClientCloseBeforeLoadUnblocksWait(t *testing.T) {
	client := &Client{done: make(chan struct{})}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := client.WaitUntilRunning(ctx)
	if !errors.Is(err, errClientClosed) {
		t.Fatalf("WaitUntilRunning() error = %v, want errClientClosed", err)
	}
}

func TestClientLoadDoesNotStartAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := &Client{
		binaryPath: "/does/not/matter",
		modelPath:  "/models/example.cmf",
		parallel:   1,
		done:       make(chan struct{}),
		client:     newHTTPClient(),
	}

	if _, err := client.Load(ctx, ml.SystemInfo{}, nil, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load() error = %v, want context.Canceled", err)
	}
	if got := client.GetPort(); got != 0 {
		t.Fatalf("Load() published port %d after cancellation, want 0", got)
	}
	if client.cmd != nil {
		t.Fatal("Load() constructed a running command after cancellation")
	}
}

func TestClientLoadStopsSidecarWhenCancelledAfterStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}

	script := writeExecutable(t, filepath.Join(t.TempDir(), "cortiq"))
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &Client{
		binaryPath: script,
		modelPath:  "/models/example.cmf",
		parallel:   1,
		done:       make(chan struct{}),
		client:     newHTTPClient(),
	}
	var started *exec.Cmd
	client.afterStart = func(cmd *exec.Cmd) {
		started = cmd
		cancel()
	}

	if _, err := client.Load(ctx, ml.SystemInfo{}, nil, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load() error = %v, want context.Canceled", err)
	}
	// A signal-terminated process may report Exited false on Unix; a non-nil
	// ProcessState is the portable proof that Load killed and waited for it.
	if started == nil || started.ProcessState == nil {
		t.Fatalf("cancelled Load() did not wait for the started sidecar to exit: %#v", started)
	}
	if client.cmd != nil {
		t.Fatal("cancelled Load() published a started sidecar")
	}
	if got := client.GetPort(); got != 0 {
		t.Fatalf("cancelled Load() retained port %d", got)
	}
	select {
	case <-client.done:
	default:
		t.Fatal("cancelled Load() did not finish the client")
	}
}

func TestCMFRunnerEnvBoundsSlotsAndDropsUnrelatedSecrets(t *testing.T) {
	t.Run("default slot", func(t *testing.T) {
		env, err := cmfRunnerEnv([]string{"HOME=/tmp/home", "OLLAMA_API_KEY=secret", "CUDA_VISIBLE_DEVICES=0"}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := environmentValue(env, "CMF_SERVE_SLOTS"); !ok || got != "1" {
			t.Fatalf("CMF_SERVE_SLOTS = %q, %v; want 1, true in %v", got, ok, env)
		}
		if _, ok := environmentValue(env, "OLLAMA_API_KEY"); ok {
			t.Fatalf("unrelated Ollama secret leaked into Cortiq environment: %v", env)
		}
		if got, ok := environmentValue(env, "CUDA_VISIBLE_DEVICES"); !ok || got != "0" {
			t.Fatalf("GPU setting = %q, %v; want 0, true", got, ok)
		}
	})

	t.Run("explicit mixed-case slot is canonicalized", func(t *testing.T) {
		env, err := cmfRunnerEnv([]string{"cmf_serve_slots=02", "CMF_THREADS=4"}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := environmentValue(env, "CMF_SERVE_SLOTS"); !ok || got != "2" {
			t.Fatalf("CMF_SERVE_SLOTS = %q, %v; want 2, true in %v", got, ok, env)
		}
		if _, ok := environmentValue(env, "cmf_serve_slots"); ok {
			t.Fatalf("mixed-case slot key was not canonicalized: %v", env)
		}
	})

	for _, value := range []string{"", "0", "not-a-number"} {
		t.Run("invalid explicit slot "+strconv.Quote(value), func(t *testing.T) {
			if _, err := cmfRunnerEnv([]string{"CMF_SERVE_SLOTS=" + value}, 1); err == nil {
				t.Fatal("cmfRunnerEnv() error = nil, want invalid slot error")
			}
		})
	}
}

func TestClientCommandUsesSharedProcessAttributes(t *testing.T) {
	t.Setenv("CMF_SERVE_SLOTS", "1")
	client := &Client{binaryPath: "/bin/echo", modelPath: "/models/example.cmf", parallel: 1}
	cmd, err := client.command(12345)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr != llm.LlamaServerSysProcAttr {
		t.Fatal("CMF command did not inherit LlamaServerSysProcAttr")
	}
}

func TestDoneReasonRejectsRuntimeError(t *testing.T) {
	got, err := doneReason("error")
	if err == nil || got != llm.DoneReasonConnectionClosed {
		t.Fatalf("doneReason(error) = %v, %v; want connection closed and an error", got, err)
	}
}

func TestDecodeCMFJSONRejectsOversizedResponse(t *testing.T) {
	var destination any
	err := decodeCMFJSON(strings.NewReader(strings.Repeat("x", maxCMFJSONResponseSize+1)), &destination)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("decodeCMFJSON() error = %v, want response-size error", err)
	}
}

func TestDecodeToolCallsAcceptsOpenAIStringArguments(t *testing.T) {
	calls, err := decodeToolCalls(json.RawMessage(`[{"id":"call_1","function":{"index":0,"name":"weather","arguments":"{\"city\":\"Istanbul\"}"}}]`))
	if err != nil {
		t.Fatalf("decodeToolCalls() error = %v", err)
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Function.Name != "weather" {
		t.Fatalf("unexpected tool calls: %#v", calls)
	}
	city, ok := calls[0].Function.Arguments.Get("city")
	if !ok || city != "Istanbul" {
		t.Fatalf("tool arguments = %#v, want city=Istanbul", calls[0].Function.Arguments)
	}
}

func TestCMFMessagesRejectsImages(t *testing.T) {
	_, err := cmfMessages([]api.Message{{Role: "user", Images: []api.ImageData{{1}}}})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("cmfMessages() error = %v, want ErrUnsupported", err)
	}
}

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func environmentValue(env []string, want string) (string, bool) {
	for _, value := range env {
		key, setting, found := strings.Cut(value, "=")
		if found && key == want {
			return setting, true
		}
	}
	return "", false
}

func ptr[T any](value T) *T { return &value }
