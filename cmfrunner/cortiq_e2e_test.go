package cmfrunner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/ml"
)

// TestCortiqDownloadedRuntimeE2E exercises the adapter against a downloaded,
// pinned Cortiq release binary and a real CPU-runnable CMF fixture. It is opt-in
// because it executes a third-party runtime; CI supplies a credential-free
// environment after independently verifying both input hashes.
func TestCortiqDownloadedRuntimeE2E(t *testing.T) {
	if os.Getenv("CMF_E2E") != "1" {
		t.Skip("set CMF_E2E=1 with verified runtime inputs to run Cortiq E2E")
	}
	binary := os.Getenv(cortiqPathEnv)
	modelPath := os.Getenv("CMF_E2E_MODEL")
	if !filepath.IsAbs(binary) || !filepath.IsAbs(modelPath) {
		t.Fatal("CMF E2E requires absolute OLLAMA_CORTIQ_PATH and CMF_E2E_MODEL")
	}

	client, err := NewClient(modelPath, 1)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if _, err := client.Load(ctx, ml.SystemInfo{}, nil, false); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := client.WaitUntilRunning(ctx); err != nil {
		t.Fatalf("WaitUntilRunning() error = %v", err)
	}
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	opts := api.DefaultOptions()
	opts.Temperature = 0
	opts.NumPredict = 2

	var completion []llm.CompletionResponse
	if err := client.Completion(ctx, llm.CompletionRequest{
		Prompt:  "hi",
		Options: &opts,
	}, func(response llm.CompletionResponse) {
		completion = append(completion, response)
	}); err != nil {
		t.Fatalf("Completion() error = %v", err)
	}
	if len(completion) != 1 || !completion[0].Done || completion[0].Content == "" {
		t.Fatalf("Completion() responses = %#v, want one non-empty finished response", completion)
	}

	var chat []llm.ChatResponse
	if err := client.Chat(ctx, llm.ChatRequest{
		Messages: []api.Message{{Role: "user", Content: "hi"}},
		Options:  &opts,
	}, func(response llm.ChatResponse) {
		chat = append(chat, response)
	}); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	var done bool
	var content strings.Builder
	for _, response := range chat {
		content.WriteString(response.Message.Content)
		done = done || response.Done
	}
	if !done || content.Len() == 0 {
		t.Fatalf("Chat() responses = %#v, want non-empty streamed content and a finished response", chat)
	}

	if err := client.Close(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Close() error = %v", err)
	}
}
