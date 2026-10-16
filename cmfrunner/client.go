// Package cmfrunner adapts the Cortiq CMF server to Ollama's runner contract.
//
// Cortiq owns CMF model execution. Ollama retains model storage, scheduling,
// and its public APIs, and starts Cortiq only on a loopback address.
package cmfrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/thinking"
)

const (
	// cmfDefaultMaxTokens bounds a response when the caller did not set
	// num_predict. Cortiq's own default is 256 tokens, which truncates most
	// chat answers; Ollama's convention is "until the model stops", so the
	// adapter sends a generous explicit budget clamped to the model context.
	cmfDefaultMaxTokens    = 32768
	maxCMFJSONResponseSize = 8 << 20
	healthProbeTimeout     = time.Second
)

var (
	// ErrRuntimeUnavailable is returned before scheduling when no Cortiq
	// executable can be found. It is deliberately actionable: CMF model blobs
	// must never be handed to llama.cpp as a fallback.
	ErrRuntimeUnavailable = errors.New("CMF runtime is not available")
	// ErrRuntimeConfiguration is returned when an explicit CMF runtime control
	// is invalid before the sidecar starts.
	ErrRuntimeConfiguration = errors.New("CMF runtime configuration is invalid")
	// ErrUnsupported marks parts of the LlamaServer interface that this CMF
	// adapter does not currently expose.
	ErrUnsupported = errors.New("operation is not supported by the CMF runner")
	// errUnexpectedRedirect prevents a local process that races the sidecar's
	// port from redirecting prompt-bearing requests to another origin.
	errUnexpectedRedirect = errors.New("CMF runner redirects are not allowed")
	errClientClosed       = errors.New("CMF runner client is closed")
)

// Client manages one Cortiq subprocess for one CMF model.
type Client struct {
	binaryPath string
	modelPath  string
	info       *Info
	port       int
	memory     atomic.Uint64
	parallel   int

	done     chan struct{}
	doneErr  error // valid after done is closed
	doneOnce sync.Once
	client   *http.Client
	status   *llm.StatusWriter

	mu     sync.Mutex
	cmd    *exec.Cmd
	closed bool
	// afterStart is a deterministic test seam for the cancellation window
	// between starting the child and publishing it to the scheduler. It is nil
	// in production.
	afterStart func(*exec.Cmd)
}

// NewClient prepares a Cortiq subprocess for one CMF model. The process is
// only started by Load. When no Cortiq runtime is present and automatic
// installation is enabled, the pinned release is downloaded first.
func NewClient(ctx context.Context, modelPath string, parallel int) (*Client, error) {
	binary, err := EnsureRuntime(ctx, nil)
	if err != nil {
		return nil, err
	}

	stat, err := os.Stat(modelPath)
	if err != nil {
		return nil, fmt.Errorf("stat CMF model %q: %w", modelPath, err)
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("CMF model %q is not a regular file", modelPath)
	}
	info, err := ReadInfoFile(modelPath)
	if err != nil {
		return nil, err
	}

	c := &Client{
		binaryPath: binary,
		modelPath:  modelPath,
		info:       info,
		parallel:   max(parallel, 1),
		done:       make(chan struct{}),
		client:     newHTTPClient(),
	}
	c.memory.Store(uint64(stat.Size()))
	return c, nil
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true, Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errUnexpectedRedirect
		},
	}
}

// Load starts Cortiq on an ephemeral loopback port. Cortiq independently
// selects CPU/GPU execution; returning no DeviceID avoids inventing GPU usage
// for the scheduler.
func (c *Client) Load(ctx context.Context, _ ml.SystemInfo, _ []ml.DeviceInfo, _ bool) ([]ml.DeviceID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errClientClosed
	}
	if c.cmd != nil {
		return nil, nil
	}

	port, err := reserveLoopbackPort()
	if err != nil {
		return nil, err
	}

	cmd, err := c.command(port)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Do not publish a probeable port until command construction has accepted
	// the sidecar environment. In particular, an invalid CMF_SERVE_SLOTS must
	// leave this client visibly unloaded rather than probing a released port.
	c.port = port
	status := llm.NewStatusWriter(os.Stderr)
	cmd.Stdout = status
	cmd.Stderr = status

	slog.Info("starting CMF runner subprocess", "model", c.modelPath, "port", port, "binary", c.binaryPath)
	if err := ctx.Err(); err != nil {
		c.port = 0
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		err = fmt.Errorf("start CMF runner: %w", err)
		c.port = 0
		c.closed = true
		c.finish(err)
		return nil, err
	}
	if err := llm.AddRunnerToJob(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		err = fmt.Errorf("assign CMF runner to runner job: %w", err)
		c.port = 0
		c.closed = true
		c.finish(err)
		return nil, err
	}
	if c.afterStart != nil {
		c.afterStart(cmd)
	}
	// Do not use exec.CommandContext here: a successfully loaded runner must
	// outlive the HTTP request that caused it to load. But a cancellation that
	// occurs during Start or runner-job assignment must not publish a new
	// sidecar after the scheduler has abandoned its request.
	if err := ctx.Err(); err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			_ = cmd.Wait()
			stopErr := fmt.Errorf("stop CMF runner after request cancellation: %w", killErr)
			c.port = 0
			c.closed = true
			c.finish(stopErr)
			return nil, stopErr
		}
		_ = cmd.Wait()
		c.port = 0
		c.closed = true
		c.finish(err)
		return nil, err
	}

	c.cmd = cmd
	c.status = status
	go func() {
		c.finish(cmd.Wait())
	}()
	return nil, nil
}

// finish publishes the terminal child state exactly once. Closing done gives
// all waiters the required happens-before edge for reading doneErr.
func (c *Client) finish(err error) {
	if c.done == nil {
		return
	}
	c.doneOnce.Do(func() {
		c.doneErr = err
		close(c.done)
	})
}

func (c *Client) command(port int) (*exec.Cmd, error) {
	env, err := cmfRunnerEnv(os.Environ(), c.parallel)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(c.binaryPath, "serve", c.modelPath, "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Env = env
	// Keep the same Windows process flags as the built-in runner. In
	// particular this prevents a console window for every Cortiq sidecar.
	cmd.SysProcAttr = llm.LlamaServerSysProcAttr
	return cmd, nil
}

// cmfRunnerEnv deliberately gives the external Cortiq process a narrow
// runtime environment instead of every secret available to the Ollama server.
// It preserves explicit CMF/GPU/runtime controls and OS loader basics only.
func cmfRunnerEnv(parent []string, parallel int) ([]string, error) {
	env := make([]string, 0, len(parent)+1)
	slotsSet := false
	for _, value := range parent {
		key, setting, found := strings.Cut(value, "=")
		if !found || !cortiqEnvAllowed(key) {
			continue
		}
		if strings.EqualFold(key, "CMF_SERVE_SLOTS") {
			if slotsSet {
				return nil, fmt.Errorf("%w: CMF_SERVE_SLOTS is set more than once", ErrRuntimeConfiguration)
			}
			slots, err := strconv.ParseUint(strings.TrimSpace(setting), 10, 0)
			if err != nil || slots == 0 {
				return nil, fmt.Errorf("%w: CMF_SERVE_SLOTS must be a positive integer", ErrRuntimeConfiguration)
			}
			slotsSet = true
			// Unix environment variable names are case-sensitive; normalize a
			// user-provided mixed-case spelling so Cortiq sees the setting.
			env = append(env, "CMF_SERVE_SLOTS="+strconv.FormatUint(slots, 10))
			continue
		}
		env = append(env, value)
	}
	if !slotsSet {
		// Cortiq otherwise defaults to multiple slots. The scheduler intentionally
		// runs one unaccounted CMF sidecar slot, including when parallel is one.
		env = append(env, "CMF_SERVE_SLOTS="+strconv.Itoa(max(parallel, 1)))
	}
	return env, nil
}

func cortiqEnvAllowed(key string) bool {
	switch strings.ToUpper(key) {
	case "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "TMP", "TEMP", "TMPDIR", "PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH", "DYLD_FALLBACK_LIBRARY_PATH":
		return true
	}
	for _, prefix := range []string{"CMF_", "CUDA_", "HIP_", "HSA_", "ROCR_", "ROCM_", "NVIDIA_", "VK_", "METAL_", "OMP_", "MKL_", "RAYON_", "RUST_"} {
		if strings.HasPrefix(strings.ToUpper(key), prefix) {
			return true
		}
	}
	return false
}

func reserveLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve loopback port for CMF runner: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if port == 0 {
		return 0, errors.New("reserved an invalid loopback port for CMF runner")
	}
	return port, nil
}

// WaitUntilRunning waits for Cortiq's loopback liveness endpoint.
func (c *Client) WaitUntilRunning(ctx context.Context) error {
	timeout := time.NewTimer(envconfig.LoadTimeout())
	defer timeout.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			if message := c.lastError(); message != "" {
				return fmt.Errorf("CMF runner failed: %s (exit: %v)", message, c.doneErr)
			}
			return fmt.Errorf("CMF runner exited unexpectedly: %w", c.doneErr)
		case <-timeout.C:
			if message := c.lastError(); message != "" {
				return fmt.Errorf("timeout waiting for CMF runner: %s", message)
			}
			return errors.New("timeout waiting for CMF runner to start")
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
			err := c.Ping(probeCtx)
			cancel()
			if err == nil {
				slog.Info("CMF runner is ready", "port", c.port)
				return nil
			}
		}
	}
}

// Ping implements llm.LlamaServer.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := decodeCMFJSON(resp.Body, &health); err != nil {
		return fmt.Errorf("decode CMF health response: %w", err)
	}
	if health.Status != "ok" {
		return fmt.Errorf("CMF runner health status is %q", health.Status)
	}
	return nil
}

// Completion forwards raw completion requests to Cortiq's OpenAI endpoint.
// Cortiq's completion endpoint is currently non-streaming, so Ollama receives
// one final callback for each request.
func (c *Client) Completion(ctx context.Context, req llm.CompletionRequest, fn func(llm.CompletionResponse)) error {
	if len(req.Media) > 0 {
		return fmt.Errorf("%w: CMF completion media", ErrUnsupported)
	}
	if req.Logprobs || req.TopLogprobs > 0 {
		return fmt.Errorf("%w: CMF logprobs", ErrUnsupported)
	}
	if formatRequested(req.Format) {
		return fmt.Errorf("%w: CMF constrained output", ErrUnsupported)
	}
	logIgnoredOptions(req.Options, false)

	payload := completionRequest{Model: filepath.Base(c.modelPath), Prompt: req.Prompt}
	payload.applyOptions(req.Options, c.ContextLength())
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode CMF completion request: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, "/v1/completions", bytes.NewReader(body))
	if err != nil {
		return c.runnerError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return responseError(resp)
	}

	var decoded completionResponse
	if err := decodeCMFJSON(resp.Body, &decoded); err != nil {
		return fmt.Errorf("decode CMF completion response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return errors.New("CMF completion response contained no choices")
	}
	choice := decoded.Choices[0]
	reason, err := doneReason(choice.FinishReason)
	if err != nil {
		return err
	}
	fn(llm.CompletionResponse{
		Content:         choice.Text,
		Done:            true,
		DoneReason:      reason,
		PromptEvalCount: int(decoded.Usage.PromptTokens),
		EvalCount:       int(decoded.Usage.CompletionTokens),
	})
	return nil
}

// Chat forwards message-shaped requests, allowing Cortiq to own its embedded
// tokenizer and chat template instead of attempting to reproduce either in Go.
func (c *Client) Chat(ctx context.Context, req llm.ChatRequest, fn func(llm.ChatResponse)) error {
	if formatRequested(req.Format) {
		return fmt.Errorf("%w: CMF constrained output", ErrUnsupported)
	}
	if req.Logprobs || req.TopLogprobs > 0 {
		return fmt.Errorf("%w: CMF chat logprobs", ErrUnsupported)
	}
	logIgnoredOptions(req.Options, true)
	messages, err := cmfMessages(req.Messages)
	if err != nil {
		return err
	}

	payload := chatRequest{
		Model:    filepath.Base(c.modelPath),
		Messages: messages,
		Tools:    req.Tools,
		Stream:   true,
	}
	payload.applyOptions(req.Options, c.ContextLength())
	if req.Think != nil {
		enabled := req.Think.Bool()
		payload.EnableThinking = &enabled
		if req.Think.IsString() {
			payload.ReasoningEffort = req.Think.Value
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode CMF chat request: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return c.runnerError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return responseError(resp)
	}
	var stop []string
	if req.Options != nil {
		stop = req.Options.Stop
	}
	if !isEventStreamContentType(resp.Header.Get("Content-Type")) {
		return c.handleChatJSON(resp.Body, stop, fn)
	}
	return c.handleChatSSE(ctx, resp.Body, stop, fn)
}

func isEventStreamContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.EqualFold(mediaType, "text/event-stream")
}

func (c *Client) handleChatJSON(body io.Reader, stop []string, fn func(llm.ChatResponse)) error {
	var decoded chatResponse
	if err := decodeCMFJSON(body, &decoded); err != nil {
		return fmt.Errorf("decode CMF chat response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return errors.New("CMF chat response contained no choices")
	}
	choice := decoded.Choices[0]
	reason, err := doneReason(choice.FinishReason)
	if err != nil {
		return err
	}
	var toolCalls []api.ToolCall
	if len(choice.Message.ToolCalls) > 0 && string(bytes.TrimSpace(choice.Message.ToolCalls)) != "null" {
		toolCalls, err = decodeToolCalls(choice.Message.ToolCalls)
		if err != nil {
			return err
		}
	}
	content := choice.Message.Content
	reasoning := choice.Message.ReasoningContent
	if reasoning == "" {
		parser := newThinkParser()
		thought, rest := parser.AddContent(content)
		reasoning, content = thought, rest
	}
	if cut, ok := cutAtStop(content, stop); ok {
		content = cut
	}
	fn(llm.ChatResponse{
		Message:         api.Message{Role: nonEmpty(choice.Message.Role, "assistant"), Content: content, Thinking: reasoning, ToolCalls: toolCalls},
		Done:            true,
		DoneReason:      reason,
		PromptEvalCount: int(decoded.Usage.PromptTokens),
		EvalCount:       int(decoded.Usage.CompletionTokens),
	})
	return nil
}

func (c *Client) handleChatSSE(ctx context.Context, body io.Reader, stop []string, fn func(llm.ChatResponse)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var usage usage
	var pendingTools []api.ToolCall
	stopper := newStopFilter(stop)
	thinker := newThinkParser()
	finished := false
	finish := func(reason llm.DoneReason) {
		if finished {
			return
		}
		finished = true
		fn(llm.ChatResponse{
			Message:         api.Message{Role: "assistant", ToolCalls: pendingTools},
			Done:            true,
			DoneReason:      reason,
			PromptEvalCount: int(usage.PromptTokens),
			EvalCount:       int(usage.CompletionTokens),
		})
	}
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode CMF chat stream chunk: %w", err)
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if len(choice.Delta.ToolCalls) > 0 && string(choice.Delta.ToolCalls) != "null" {
				calls, err := decodeToolCalls(choice.Delta.ToolCalls)
				if err != nil {
					return err
				}
				pendingTools = append(pendingTools, calls...)
			}
			if choice.Delta.ReasoningContent != "" {
				fn(llm.ChatResponse{Message: api.Message{
					Role:     nonEmpty(choice.Delta.Role, "assistant"),
					Thinking: choice.Delta.ReasoningContent,
				}})
			}
			if choice.Delta.Content != "" {
				thought, visible := thinker.AddContent(choice.Delta.Content)
				if thought != "" {
					fn(llm.ChatResponse{Message: api.Message{
						Role:     nonEmpty(choice.Delta.Role, "assistant"),
						Thinking: thought,
					}})
				}
				if visible == "" {
					continue
				}
				text, hitStop := stopper.push(visible)
				if text != "" {
					fn(llm.ChatResponse{Message: api.Message{
						Role:    nonEmpty(choice.Delta.Role, "assistant"),
						Content: text,
					}})
				}
				if hitStop {
					// A stop sequence ends the answer; the sidecar request is
					// abandoned when the HTTP body is closed by the caller.
					finish(llm.DoneReasonStop)
					return nil
				}
			}
			if choice.FinishReason != nil {
				reason, err := doneReason(*choice.FinishReason)
				if err != nil {
					return err
				}
				if tail := stopper.flush(); tail != "" {
					fn(llm.ChatResponse{Message: api.Message{Role: "assistant", Content: tail}})
				}
				finish(reason)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read CMF chat stream: %w", err)
	}
	if !finished {
		return errors.New("CMF chat stream ended without a finish chunk")
	}
	return nil
}

// newThinkParser separates a leading <think>...</think> block, which Cortiq
// streams inline for reasoning models, into Ollama's thinking field.
func newThinkParser() *thinking.Parser {
	return &thinking.Parser{OpeningTag: "<think>", ClosingTag: "</think>"}
}

// stopFilter cuts a streamed answer at the first Ollama stop sequence. Cortiq
// has no stop parameter of its own, so the adapter holds back as many bytes
// as the longest stop sequence could span across chunk boundaries.
type stopFilter struct {
	stops   []string
	holdLen int
	pending string
}

func newStopFilter(stops []string) *stopFilter {
	f := &stopFilter{}
	for _, s := range stops {
		if s == "" {
			continue
		}
		f.stops = append(f.stops, s)
		f.holdLen = max(f.holdLen, len(s)-1)
	}
	return f
}

// push adds chunk and returns the text that is safe to emit now and whether
// a stop sequence was reached.
func (f *stopFilter) push(chunk string) (string, bool) {
	if len(f.stops) == 0 {
		return chunk, false
	}
	f.pending += chunk
	if cut, ok := cutAtStop(f.pending, f.stops); ok {
		f.pending = ""
		return cut, true
	}
	if len(f.pending) <= f.holdLen {
		return "", false
	}
	emit := f.pending[:len(f.pending)-f.holdLen]
	// Do not split a multi-byte rune across emissions.
	for len(emit) > 0 && emit[len(emit)-1] >= 0x80 && emit[len(emit)-1] < 0xC0 {
		emit = emit[:len(emit)-1]
	}
	f.pending = f.pending[len(emit):]
	return emit, false
}

// flush returns held-back text when the stream ends without a stop.
func (f *stopFilter) flush() string {
	out := f.pending
	f.pending = ""
	return out
}

// cutAtStop returns text truncated before the earliest stop sequence.
func cutAtStop(text string, stops []string) (string, bool) {
	cut := -1
	for _, s := range stops {
		if s == "" {
			continue
		}
		if i := strings.Index(text, s); i >= 0 && (cut < 0 || i < cut) {
			cut = i
		}
	}
	if cut < 0 {
		return text, false
	}
	return text[:cut], true
}

func decodeToolCalls(raw json.RawMessage) ([]api.ToolCall, error) {
	var direct []api.ToolCall
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, nil
	}

	var calls []api.ToolCall
	// Some OpenAI-compatible servers serialize function arguments as a JSON
	// string. Translate it deliberately instead of silently dropping calls.
	var wire []struct {
		ID       string `json:"id"`
		Function struct {
			Index     int             `json:"index"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("decode CMF tool calls: %w", err)
	}
	for _, call := range wire {
		arguments := api.NewToolCallFunctionArguments()
		argumentJSON := call.Function.Arguments
		if len(argumentJSON) > 0 && argumentJSON[0] == '"' {
			var text string
			if err := json.Unmarshal(argumentJSON, &text); err != nil {
				return nil, fmt.Errorf("decode CMF tool call arguments: %w", err)
			}
			argumentJSON = []byte(text)
		}
		if len(argumentJSON) > 0 && string(argumentJSON) != "null" {
			if err := json.Unmarshal(argumentJSON, &arguments); err != nil {
				return nil, fmt.Errorf("decode CMF tool call arguments: %w", err)
			}
		}
		calls = append(calls, api.ToolCall{
			ID: call.ID,
			Function: api.ToolCallFunction{
				Index:     call.Function.Index,
				Name:      call.Function.Name,
				Arguments: arguments,
			},
		})
	}
	return calls, nil
}

// ApplyChatTemplate cannot be faithfully implemented against Cortiq's public
// API. Normal CMF requests use Chat directly, so this only affects diagnostic
// template rendering and callers that require token-accurate truncation.
func (c *Client) ApplyChatTemplate(context.Context, llm.ChatRequest) (string, error) {
	return "", fmt.Errorf("%w: CMF chat templates are rendered inside Cortiq", ErrUnsupported)
}

// Tokenize cannot be faithfully implemented until Cortiq exposes its tokenizer
// protocol endpoint. CMF chat execution deliberately avoids this method.
func (c *Client) Tokenize(context.Context, string) ([]int, error) {
	return nil, fmt.Errorf("%w: CMF tokenization", ErrUnsupported)
}

// Detokenize cannot be faithfully implemented until Cortiq exposes its tokenizer
// protocol endpoint.
func (c *Client) Detokenize(context.Context, []int) (string, error) {
	return "", fmt.Errorf("%w: CMF detokenization", ErrUnsupported)
}

// Embedding is intentionally unsupported until CMF exposes a compatible
// endpoint for a model that declares the embedding capability.
func (c *Client) Embedding(context.Context, string, [][]byte) ([]float32, int, error) {
	return nil, 0, fmt.Errorf("%w: CMF embeddings", ErrUnsupported)
}

func (c *Client) ModelPath() string { return c.modelPath }

func (c *Client) Pid() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Pid
	}
	return -1
}

func (c *Client) GetPort() int { return c.port }

func (c *Client) HasExited() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// ContextLength is the context window declared in the CMF header. Cortiq
// owns the context policy; Ollama only reports the number.
func (c *Client) ContextLength() int {
	if c.info == nil {
		return 0
	}
	return c.info.ContextLength()
}

// Info returns the metadata read from the CMF file.
func (c *Client) Info() *Info { return c.info }

func (c *Client) GetDeviceInfos(context.Context) []ml.DeviceInfo { return nil }

func (c *Client) MemorySize() (total, vram uint64) { return c.memory.Load(), 0 }

func (c *Client) VRAMByGPU(ml.DeviceID) uint64 { return 0 }

// Close terminates the owned sidecar. A SIGKILL is intentional: the child is
// loopback-only and has no persistent state that needs an HTTP shutdown.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.cmd == nil || c.cmd.Process == nil {
		c.finish(errClientClosed)
		return nil
	}
	slog.Info("stopping CMF runner subprocess", "pid", c.cmd.Process.Pid)
	if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-c.done
	c.cmd = nil
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if c.port == 0 {
		return nil, errors.New("CMF runner has not been loaded")
	}
	// Never reuse a released loopback port after the process we started has
	// exited. Another local process could otherwise bind it and impersonate the
	// sidecar during scheduler health checks or prompt requests. This does not
	// solve the remaining bind/request TOCTOU; that needs Cortiq authentication
	// or an inherited listener, both outside the current public protocol.
	select {
	case <-c.done:
		if c.doneErr != nil {
			return nil, fmt.Errorf("CMF runner exited: %w", c.doneErr)
		}
		return nil, errors.New("CMF runner exited")
	default:
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(c.port)+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.client.Do(req)
}

func (c *Client) runnerError(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	if message := c.lastError(); message != "" {
		return fmt.Errorf("CMF runner stopped: %s", message)
	}
	return err
}

func (c *Client) lastError() string {
	if c.status == nil {
		return ""
	}
	return c.status.LastError()
}

func cmfMessages(messages []api.Message) ([]cmfMessage, error) {
	out := make([]cmfMessage, 0, len(messages))
	for _, message := range messages {
		if len(message.Images) > 0 {
			return nil, fmt.Errorf("%w: CMF image messages", ErrUnsupported)
		}
		out = append(out, cmfMessage{
			Role:             message.Role,
			Content:          message.Content,
			ToolCalls:        message.ToolCalls,
			ToolCallID:       message.ToolCallID,
			Name:             message.ToolName,
			ReasoningContent: message.Thinking,
		})
	}
	return out, nil
}

func formatRequested(format json.RawMessage) bool {
	format = bytes.TrimSpace(format)
	return len(format) > 0 && string(format) != "null" && string(format) != `""`
}

func doneReason(reason string) (llm.DoneReason, error) {
	switch reason {
	case "", "stop", "tool_calls":
		return llm.DoneReasonStop, nil
	case "length", "max_tokens":
		return llm.DoneReasonLength, nil
	case "cancelled", "error":
		return llm.DoneReasonConnectionClosed, fmt.Errorf("CMF generation ended with %q", reason)
	default:
		return llm.DoneReasonConnectionClosed, fmt.Errorf("CMF generation ended with an unknown finish reason %q", reason)
	}
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// completionSamplingOptions is what Cortiq's /v1/completions accepts.
type completionSamplingOptions struct {
	Temperature *float32 `json:"temperature,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
}

func (s *completionSamplingOptions) applyOptions(options *api.Options, contextLength int) {
	numPredict := -1
	if options != nil {
		s.Temperature = &options.Temperature
		numPredict = options.NumPredict
	}
	count := cmfMaxTokens(numPredict, contextLength)
	s.MaxTokens = &count
}

// chatSamplingOptions is what Cortiq's /v1/chat/completions accepts.
type chatSamplingOptions struct {
	Temperature *float32 `json:"temperature,omitempty"`
	TopP        *float32 `json:"top_p,omitempty"`
	Seed        *uint64  `json:"seed,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
}

func (s *chatSamplingOptions) applyOptions(options *api.Options, contextLength int) {
	numPredict := -1
	if options != nil {
		s.Temperature = &options.Temperature
		if options.TopP > 0 && options.TopP <= 1 {
			s.TopP = &options.TopP
		}
		if options.Seed >= 0 {
			seed := uint64(options.Seed)
			s.Seed = &seed
		}
		numPredict = options.NumPredict
	}
	count := cmfMaxTokens(numPredict, contextLength)
	s.MaxTokens = &count
}

// cmfMaxTokens maps Ollama's num_predict to Cortiq's max_tokens. Negative
// values mean "no explicit limit" in Ollama; Cortiq always needs a number, so
// the adapter sends a large budget bounded by the model's context window.
func cmfMaxTokens(numPredict, contextLength int) int {
	if numPredict >= 0 {
		return numPredict
	}
	limit := cmfDefaultMaxTokens
	if contextLength > 0 && contextLength < limit {
		limit = contextLength
	}
	return limit
}

// logIgnoredOptions records request options Cortiq's HTTP API has no
// equivalent for. They are ignored rather than rejected so ordinary clients
// that always send them (num_ctx, top_k, repeat_penalty, ...) keep working.
func logIgnoredOptions(options *api.Options, chat bool) {
	if options == nil {
		return
	}
	defaults := api.DefaultOptions()
	var ignored []string
	if options.TopK != defaults.TopK {
		ignored = append(ignored, "top_k")
	}
	if options.MinP != defaults.MinP {
		ignored = append(ignored, "min_p")
	}
	if options.RepeatPenalty != defaults.RepeatPenalty {
		ignored = append(ignored, "repeat_penalty")
	}
	if options.PresencePenalty != defaults.PresencePenalty {
		ignored = append(ignored, "presence_penalty")
	}
	if options.FrequencyPenalty != defaults.FrequencyPenalty {
		ignored = append(ignored, "frequency_penalty")
	}
	if !chat {
		if options.TopP != defaults.TopP {
			ignored = append(ignored, "top_p")
		}
		if options.Seed != defaults.Seed {
			ignored = append(ignored, "seed")
		}
		if len(options.Stop) > 0 {
			ignored = append(ignored, "stop")
		}
	}
	if len(ignored) > 0 {
		slog.Debug("CMF runner ignores options Cortiq does not expose", "options", strings.Join(ignored, ", "))
	}
}

type completionRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	completionSamplingOptions
}

type chatRequest struct {
	Model           string       `json:"model"`
	Messages        []cmfMessage `json:"messages"`
	Tools           api.Tools    `json:"tools,omitempty"`
	Stream          bool         `json:"stream"`
	EnableThinking  *bool        `json:"enable_thinking,omitempty"`
	ReasoningEffort any          `json:"reasoning_effort,omitempty"`
	chatSamplingOptions
}

type cmfMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ToolCalls        []api.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	Name             string         `json:"name,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
}

type usage struct {
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
}

type completionResponse struct {
	Choices []struct {
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage usage `json:"usage"`
}

type chatResponse struct {
	Choices []struct {
		Message      cmfResponseMessage `json:"message"`
		FinishReason string             `json:"finish_reason"`
	} `json:"choices"`
	Usage usage `json:"usage"`
}

// cmfResponseMessage keeps tool_calls raw until decodeToolCalls can normalize
// OpenAI-compatible servers that encode function arguments as a JSON string.
type cmfResponseMessage struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ToolCalls        json.RawMessage `json:"tool_calls"`
	ReasoningContent string          `json:"reasoning_content"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Role             string          `json:"role"`
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        json.RawMessage `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usage `json:"usage"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func responseError(resp *http.Response) error {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return fmt.Errorf("read CMF error response: %w", readErr)
	}
	var payload apiError
	if json.Unmarshal(body, &payload) == nil && payload.Error.Message != "" {
		return api.StatusError{StatusCode: resp.StatusCode, ErrorMessage: payload.Error.Message}
	}
	return api.StatusError{StatusCode: resp.StatusCode, ErrorMessage: strings.TrimSpace(string(body))}
}

func decodeCMFJSON(body io.Reader, destination any) error {
	data, err := io.ReadAll(io.LimitReader(body, maxCMFJSONResponseSize+1))
	if err != nil {
		return err
	}
	if len(data) > maxCMFJSONResponseSize {
		return fmt.Errorf("CMF JSON response exceeds %d bytes", maxCMFJSONResponseSize)
	}
	return json.Unmarshal(data, destination)
}

var _ llm.LlamaServer = (*Client)(nil)
