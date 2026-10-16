package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/llm"
	"github.com/ollama/ollama/manifest"
	"github.com/ollama/ollama/ml"
	"github.com/ollama/ollama/types/model"
)

func createCMFTestBlob(t *testing.T, payload string) string {
	t.Helper()
	return createTestBlob(t, cmfTestEnvelope(payload))
}

func TestCMFRegistryPolicyErrorsAreBadRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, policyErr := range []error{errCMFRegistryPush} {
		t.Run(policyErr.Error(), func(t *testing.T) {
			response := registryOperationError(policyErr)
			if got, ok := response["status"].(int); !ok || got != http.StatusBadRequest {
				t.Fatalf("response status = %#v, want %d", response["status"], http.StatusBadRequest)
			}

			for _, streamed := range []bool{false, true} {
				name := "non-streaming"
				if streamed {
					name = "streaming"
				}
				t.Run(name, func(t *testing.T) {
					w := NewRecorder()
					c, _ := gin.CreateTestContext(w)
					ch := make(chan any, 1)
					ch <- response
					close(ch)
					if streamed {
						streamResponse(c, ch)
					} else {
						waitForStream(c, ch)
					}
					if w.Code != http.StatusBadRequest {
						t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusBadRequest, w.Body.String())
					}
					if got, want := strings.TrimSpace(w.Body.String()), `{"error":`+strconv.Quote(policyErr.Error())+`}`; got != want {
						t.Fatalf("response = %s, want %s", got, want)
					}
				})
			}
		})
	}
}

// CMF policy errors deliberately precede progress so a handler can return a
// useful 4xx. That exception must not turn ordinary preflight failures into
// synchronous 500 responses for streaming clients: before CMF support, the
// initial registry progress record committed the normal 200/NDJSON envelope.
func TestNonCMFRegistryPreflightErrorsKeepStreamingProgress(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("pull manifest failure", func(t *testing.T) {
		t.Setenv("OLLAMA_MODELS", t.TempDir())
		registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "missing ordinary manifest", http.StatusNotFound)
		}))
		defer registry.Close()

		name := strings.TrimPrefix(registry.URL, "http://") + "/library/ordinary:latest"
		var s Server
		w := createRequest(t, s.PullHandler, api.PullRequest{
			Model:    name,
			Insecure: true,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("streaming pull status = %d, want 200: %s", w.Code, w.Body.String())
		}
		if got := w.Body.String(); !strings.Contains(got, `"status":"pulling manifest"`) || !strings.Contains(got, `"error":`) {
			t.Fatalf("streaming pull response = %q, want progress followed by error", got)
		}
	})

	t.Run("push unreadable ordinary config", func(t *testing.T) {
		t.Setenv("OLLAMA_MODELS", t.TempDir())
		name := model.ParseName("registry.invalid/library/ordinary:latest")
		data, err := json.Marshal(manifest.Manifest{
			SchemaVersion: 2,
			MediaType:     manifest.MediaTypeManifest,
			Runner:        manifest.RunnerLlamaCPP,
			Format:        manifest.FormatGGUF,
			Config: manifest.Layer{
				MediaType: manifest.MediaTypeImageConfig,
				Digest:    "sha256:" + strings.Repeat("a", 64),
				Size:      1,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := manifest.WriteManifestData(name, data); err != nil {
			t.Fatal(err)
		}

		var s Server
		w := createRequest(t, s.PushHandler, api.PushRequest{
			Model:    name.String(),
			Insecure: true,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("streaming push status = %d, want 200: %s", w.Code, w.Body.String())
		}
		if got := w.Body.String(); !strings.Contains(got, `"status":"retrieving manifest"`) || !strings.Contains(got, `"error":`) {
			t.Fatalf("streaming push response = %q, want progress followed by error", got)
		}
	})
}

// cmfTestEnvelope is only a container-level fixture. It exercises the
// adapter's import validation without pretending to be a runnable neural
// network; Cortiq remains responsible for validating the opaque payload.
const (
	cmfTestMagic          = "CMF\x01"
	cmfTestEnvelopeLength = 128
	cmfTestVersion        = 2
)

func cmfTestEnvelope(payload string) []byte {
	header := []byte(`{"format":"cmf","version":2,"arch":{"arch_name":"tiny-qwen","hidden_size":32,"num_layers":2,"vocab_size":128,"max_position_embeddings":128},"quant_type":"Q4_BLOCK","fixture":` + strconv.Quote(payload) + `}`)
	directory := make([]byte, 16) // zero records, with a pool immediately after the fixed header
	binary.LittleEndian.PutUint64(directory[8:16], uint64(len(directory)))

	directoryOffset := cmfTestEnvelopeLength + len(header)
	contents := make([]byte, directoryOffset+len(directory))
	copy(contents, cmfTestMagic)
	binary.LittleEndian.PutUint32(contents[4:8], cmfTestVersion)
	binary.LittleEndian.PutUint64(contents[0x10:0x18], cmfTestEnvelopeLength)
	binary.LittleEndian.PutUint64(contents[0x18:0x20], uint64(len(header)))
	binary.LittleEndian.PutUint64(contents[0x20:0x28], uint64(directoryOffset))
	binary.LittleEndian.PutUint64(contents[0x28:0x30], uint64(len(directory)))
	copy(contents[cmfTestEnvelopeLength:], header)
	copy(contents[directoryOffset:], directory)
	return contents
}

func TestDetectCMFModelType(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	valid := createCMFTestBlob(t, "opaque model")
	invalid := createTestBlob(t, []byte("not a CMF model"))
	truncated := createTestBlob(t, []byte(cmfTestMagic))
	unsupportedVersion := cmfTestEnvelope("unsupported version")
	binary.LittleEndian.PutUint32(unsupportedVersion[4:8], cmfTestVersion+1)
	unsupportedVersionDigest := createTestBlob(t, unsupportedVersion)
	unsupportedFeature := cmfTestEnvelope("unsupported feature")
	binary.LittleEndian.PutUint32(unsupportedFeature[12:16], 1<<3) // DELTA_MASKS is not supported by the pinned Cortiq runtime.
	unsupportedFeatureDigest := createTestBlob(t, unsupportedFeature)
	boundedState := cmfTestEnvelope("bounded state feature")
	binary.LittleEndian.PutUint32(boundedState[12:16], 1<<12)
	boundedStateDigest := createTestBlob(t, boundedState)
	genome := cmfTestEnvelope("genome feature")
	binary.LittleEndian.PutUint32(genome[12:16], 1<<13)
	genomeDigest := createTestBlob(t, genome)
	outOfBounds := cmfTestEnvelope("out of bounds")
	binary.LittleEndian.PutUint64(outOfBounds[0x20:0x28], uint64(len(outOfBounds)+1))
	outOfBoundsDigest := createTestBlob(t, outOfBounds)
	tests := []struct {
		name    string
		file    string
		digest  string
		want    string
		wantErr error
	}{
		{name: "cmf extension", file: "model.cmf", digest: valid, want: manifest.FormatCMF},
		{name: "upper case cmf extension", file: "model.CMF", digest: valid, want: manifest.FormatCMF},
		{name: "cmf magic without extension", file: "model", digest: valid, want: manifest.FormatCMF},
		{name: "invalid cmf header", file: "model.cmf", digest: invalid, wantErr: errInvalidCMF},
		{name: "truncated cmf envelope", file: "model.cmf", digest: truncated, wantErr: errInvalidCMF},
		{name: "unsupported cmf version", file: "model.cmf", digest: unsupportedVersionDigest, wantErr: errInvalidCMF},
		{name: "unsupported cmf required feature", file: "model.cmf", digest: unsupportedFeatureDigest, wantErr: errInvalidCMF},
		{name: "bounded state CMF required feature", file: "model.cmf", digest: boundedStateDigest, want: manifest.FormatCMF},
		{name: "genome CMF required feature", file: "model.cmf", digest: genomeDigest, want: manifest.FormatCMF},
		{name: "out of bounds cmf section", file: "model.cmf", digest: outOfBoundsDigest, wantErr: errInvalidCMF},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := detectModelTypeFromFiles(map[string]string{tt.file: tt.digest})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("detectModelTypeFromFiles() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("model type = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateHandlerImportsCMF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	var s Server
	digest := createCMFTestBlob(t, "opaque model")
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model: "cmf-import",
		Files: map[string]string{
			"model.cmf": digest,
		},
		// CMF is intentionally completion-only for now, even if a caller tries
		// to assert extra capabilities through either create field.
		Capabilities: []string{"vision"},
		Info:         map[string]any{"capabilities": []string{"embedding"}},
		Stream:       &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	mf, err := manifest.ParseNamedManifest(model.ParseName("cmf-import"))
	if err != nil {
		t.Fatal(err)
	}
	if mf.Runner != manifest.RunnerCMF || mf.Format != manifest.FormatCMF {
		t.Fatalf("manifest metadata = runner %q format %q, want %q/%q", mf.Runner, mf.Format, manifest.RunnerCMF, manifest.FormatCMF)
	}

	m, err := GetModel("cmf-import")
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsCMF() {
		t.Fatal("imported model is not CMF")
	}
	if got, want := m.Config.Capabilities, []string{string(model.CapabilityCompletion)}; !slices.Equal(got, want) {
		t.Fatalf("stored capabilities = %v, want %v", got, want)
	}
	if got, want := m.Capabilities(), []model.Capability{model.CapabilityCompletion}; !slices.Equal(got, want) {
		t.Fatalf("runtime capabilities = %v, want %v", got, want)
	}

	// CMF must not be parsed as GGUF by either SHOW or a derived model create.
	shown, err := GetModelInfo(api.ShowRequest{Model: "cmf-import"})
	if err != nil {
		t.Fatalf("GetModelInfo() error = %v", err)
	}
	if !slices.Equal(shown.Capabilities, []model.Capability{model.CapabilityCompletion}) {
		t.Fatalf("show capabilities = %v, want completion only", shown.Capabilities)
	}
	if !strings.Contains(shown.Modelfile, "FROM cmf-import:latest\n") {
		t.Fatalf("show Modelfile = %q, want CMF short-name FROM", shown.Modelfile)
	}
	if shown.Template != "" || strings.Contains(shown.Modelfile, "TEMPLATE") {
		t.Fatalf("CMF show output must not advertise an ignored Ollama template: template=%q modelfile=%q", shown.Template, shown.Modelfile)
	}

	w = createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:  "cmf-derived",
		From:   "cmf-import",
		Stream: &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create derived status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
	derived, err := GetModel("cmf-derived")
	if err != nil {
		t.Fatal(err)
	}
	if !derived.IsCMF() {
		t.Fatal("derived model is not CMF")
	}
}

// The checked-in integration fixture must satisfy the same local-import gate
// as a user-provided CMF model. The adapter E2E executes it separately; this
// test protects the container-envelope path from drifting away from the real
// pinned artifact while keeping unit tests independent of a Cortiq binary.
func TestCreateHandlerImportsPinnedCMFFixture(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	fixture, err := os.ReadFile("../testdata/cmf/tiny-v0.8.19.cmf")
	if err != nil {
		t.Fatal(err)
	}
	var s Server
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:  "cmf-pinned-fixture",
		Files:  map[string]string{"tiny-v0.8.19.cmf": createTestBlob(t, fixture)},
		Stream: &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
	m, err := GetModel("cmf-pinned-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsCMF() || m.Runner != manifest.RunnerCMF {
		t.Fatalf("fixture model = format %q runner %q, want CMF/%q", m.Config.ModelFormat, m.Runner, manifest.RunnerCMF)
	}
}

func TestCreateHandlerRejectsInvalidCMF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	var s Server
	invalid := createTestBlob(t, []byte("not a CMF model"))
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:  "invalid-cmf",
		Files:  map[string]string{"model.cmf": invalid},
		Stream: &stream,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != errInvalidCMF.Error() {
		t.Fatalf("error = %q, want %q", response.Error, errInvalidCMF)
	}
}

func TestCreateHandlerRejectsUnsupportedCMFLayouts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	var s Server
	digest := createCMFTestBlob(t, "opaque model")
	tests := []struct {
		name string
		req  api.CreateRequest
		want string
	}{
		{
			name: "multiple CMF files",
			req: api.CreateRequest{Files: map[string]string{
				"first.cmf":  digest,
				"second.cmf": digest,
			}},
			want: errCMFMultipleFiles.Error(),
		},
		{
			name: "draft CMF",
			req: api.CreateRequest{
				Files:      map[string]string{"model.cmf": digest},
				DraftFiles: map[string]string{"draft.cmf": digest},
			},
			want: errCMFDraftUnsupported.Error(),
		},
		{
			name: "quantized CMF",
			req: api.CreateRequest{
				Files:    map[string]string{"model.cmf": digest},
				Quantize: "int4",
			},
			want: errCMFQuantization.Error(),
		},
		{
			name: "CMF template",
			req: api.CreateRequest{
				Files:    map[string]string{"model.cmf": digest},
				Template: "{{ .Prompt }}",
			},
			want: errCMFTemplateUnsupported.Error(),
		},
		{
			name: "CMF renderer",
			req: api.CreateRequest{
				Files:    map[string]string{"model.cmf": digest},
				Renderer: "example",
			},
			want: errCMFFormattingUnsupported.Error(),
		},
		{
			name: "CMF image message",
			req: api.CreateRequest{
				Files:    map[string]string{"model.cmf": digest},
				Messages: []api.Message{{Role: "user", Images: []api.ImageData{{1}}}},
			},
			want: errCMFImagesUnsupported.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.Model = "invalid-cmf-" + strings.ReplaceAll(strings.ToLower(tt.name), " ", "-")
			tt.req.Stream = &stream
			w := createRequest(t, s.CreateHandler, tt.req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusBadRequest, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.want) {
				t.Fatalf("error = %s, want %q", w.Body.String(), tt.want)
			}
		})
	}
}

func TestCreateHandlerRejectsUnsupportedCMFDerivedConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	var s Server
	digest := createCMFTestBlob(t, "opaque model")
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:  "cmf-base",
		Files:  map[string]string{"model.cmf": digest},
		Stream: &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("base create status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	w = createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:    "cmf-derived-template",
		From:     "cmf-base",
		Template: "{{ .Prompt }}",
		Stream:   &stream,
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errCMFTemplateUnsupported.Error()) {
		t.Fatalf("derived template status/body = %d/%s, want 400/%q", w.Code, w.Body.String(), errCMFTemplateUnsupported)
	}
}

// A legacy or manually constructed CMF manifest may contain Modelfile layers
// that today's local importer refuses. FROM must validate the layers that will
// survive the derivation instead of creating a model which every later request
// would reject before it reaches Cortiq.
func TestCreateHandlerRejectsUnsupportedInheritedCMFConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newCMFModelLayer := func(t *testing.T) manifest.Layer {
		t.Helper()
		layer, err := manifest.NewLayerFromLayer(
			createCMFTestBlob(t, "opaque inherited model"), manifest.MediaTypeImageModel, "model.cmf",
		)
		if err != nil {
			t.Fatal(err)
		}
		return layer
	}
	newTextLayer := func(t *testing.T, value, mediaType string) manifest.Layer {
		t.Helper()
		layer, err := manifest.NewLayer(strings.NewReader(value), mediaType)
		if err != nil {
			t.Fatal(err)
		}
		return layer
	}

	for _, tt := range []struct {
		name        string
		extraLayers func(*testing.T) []manifest.Layer
		want        error
	}{
		{
			name: "image messages",
			extraLayers: func(t *testing.T) []manifest.Layer {
				return []manifest.Layer{newTextLayer(t, `[{"role":"user","images":["AQ=="]}]`, manifest.MediaTypeImageMessages)}
			},
			want: errCMFImagesUnsupported,
		},
		{
			name: "adapter",
			extraLayers: func(t *testing.T) []manifest.Layer {
				return []manifest.Layer{newTextLayer(t, "unsupported adapter", manifest.MediaTypeImageAdapter)}
			},
			want: errCMFInvalidConfiguration,
		},
		{
			name: "multiple model layers",
			extraLayers: func(t *testing.T) []manifest.Layer {
				return []manifest.Layer{newCMFModelLayer(t)}
			},
			want: errCMFInvalidConfiguration,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OLLAMA_MODELS", t.TempDir())
			base := "cmf-inherited-" + strings.ReplaceAll(tt.name, " ", "-")
			destination := base + "-derived"
			layers := append([]manifest.Layer{newCMFModelLayer(t)}, tt.extraLayers(t)...)
			writeShowManifestVariant(t, base, manifest.RunnerCMF, manifest.FormatCMF,
				model.ConfigV2{ModelFormat: manifest.FormatCMF, Capabilities: []string{string(model.CapabilityCompletion)}},
				nil, layers...)

			var s Server
			w := createRequest(t, s.CreateHandler, api.CreateRequest{
				Model:  destination,
				From:   base,
				Stream: &stream,
			})
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tt.want.Error()) {
				t.Fatalf("derived CMF status/body = %d/%s, want 400 containing %q", w.Code, w.Body.String(), tt.want)
			}
			if _, err := manifest.ReadManifestData(model.ParseName(destination)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("derived CMF manifest was written despite rejected inherited configuration: %v", err)
			}
		})
	}

}

func TestCMFRunnerAliasesAndChatMode(t *testing.T) {
	for _, runner := range []string{"cmf", "cmfrunner", "cmf-runner", "cmf_runner", "cortiq", "  CMFRunner  "} {
		got, err := normalizeRunner(runner)
		if err != nil {
			t.Fatalf("normalizeRunner(%q): %v", runner, err)
		}
		if got != manifest.RunnerCMF {
			t.Fatalf("normalizeRunner(%q) = %q, want %q", runner, got, manifest.RunnerCMF)
		}
	}

	cmf := &Model{Config: model.ConfigV2{
		ModelFormat: manifest.FormatCMF,
		Renderer:    "example-renderer",
		Parser:      "example-parser",
	}}
	if got := chatModeForModel(cmf); got != chatExecutionModeNative {
		t.Fatalf("chatModeForModel(CMF) = %v, want native", got)
	}
}

func TestCMFNativeChatDoesNotTemplateOrTruncate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mock := &mockRunner{
		TemplateFn: func(context.Context, llm.ChatRequest) (string, error) {
			t.Fatal("CMF native chat must not call ApplyChatTemplate")
			return "", nil
		},
		ChatFn: func(_ context.Context, req llm.ChatRequest, fn func(llm.ChatResponse)) error {
			fn(llm.ChatResponse{
				Message:    api.Message{Role: "assistant", Content: "ok"},
				Done:       true,
				DoneReason: llm.DoneReasonStop,
			})
			return nil
		},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	msgs := []api.Message{
		{Role: "user", Content: "first"},
		{Role: "user", Content: "second"},
	}
	m := &Model{Config: model.ConfigV2{ModelFormat: manifest.FormatCMF}}
	var s Server
	s.handleNativeChat(c, api.ChatRequest{Model: "cmf", Stream: &stream}, m, mock,
		&api.Options{Runner: api.Runner{NumCtx: 1}}, msgs, time.Now(), time.Now())

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if !reflect.DeepEqual(mock.ChatRequest.Messages, msgs) {
		t.Fatalf("CMF chat messages = %#v, want %#v", mock.ChatRequest.Messages, msgs)
	}
	if mock.ChatRequest.Shift {
		t.Fatal("CMF native chat must not advertise an implicit Ollama context shift")
	}
}

func TestCMFNativeChatRejectsDebugRendering(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mock := &mockRunner{
		TemplateFn: func(context.Context, llm.ChatRequest) (string, error) {
			t.Fatal("CMF debug rendering must not call ApplyChatTemplate")
			return "", nil
		},
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	m := &Model{Config: model.ConfigV2{ModelFormat: manifest.FormatCMF}}
	var s Server
	s.handleNativeChat(c, api.ChatRequest{Model: "cmf", DebugRenderOnly: true}, m, mock,
		&api.Options{}, []api.Message{{Role: "user", Content: "hello"}}, time.Now(), time.Now())

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestCMFGenerateUsesNativeChatForFormattedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	mock := &mockRunner{
		CompletionFn: func(context.Context, llm.CompletionRequest, func(llm.CompletionResponse)) error {
			t.Fatal("formatted CMF generate must not call Completion")
			return nil
		},
		ChatFn: func(_ context.Context, req llm.ChatRequest, fn func(llm.ChatResponse)) error {
			fn(llm.ChatResponse{
				Message:    api.Message{Role: "assistant", Content: "native response"},
				Done:       true,
				DoneReason: llm.DoneReasonStop,
			})
			return nil
		},
	}
	s := newServerWithMockRunner(t, mock)
	digest := createCMFTestBlob(t, "opaque model")
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:    "cmf-native-generate",
		Files:    map[string]string{"model.cmf": digest},
		System:   "model system",
		Messages: []api.Message{{Role: "user", Content: "model message"}},
		Stream:   &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	w = createRequest(t, s.GenerateHandler, api.GenerateRequest{
		Model:  "cmf-native-generate",
		Prompt: "request prompt",
		System: "request system",
		Stream: &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("generate status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
	want := []api.Message{
		{Role: "system", Content: "request system"},
		{Role: "user", Content: "model message"},
		{Role: "user", Content: "request prompt"},
	}
	if !reflect.DeepEqual(mock.ChatRequest.Messages, want) {
		t.Fatalf("CMF native generate messages = %#v, want %#v", mock.ChatRequest.Messages, want)
	}
	var response api.GenerateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Response != "native response" || !response.Done || response.DoneReason != llm.DoneReasonStop.String() {
		t.Fatalf("generate response = %#v", response)
	}
}

func TestCMFRawGenerateUsesCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	mock := &mockRunner{
		ChatFn: func(context.Context, llm.ChatRequest, func(llm.ChatResponse)) error {
			t.Fatal("raw CMF generate must not call Chat")
			return nil
		},
		CompletionFn: func(_ context.Context, req llm.CompletionRequest, fn func(llm.CompletionResponse)) error {
			fn(llm.CompletionResponse{Content: "raw response", Done: true, DoneReason: llm.DoneReasonStop})
			return nil
		},
	}
	s := newServerWithMockRunner(t, mock)
	digest := createCMFTestBlob(t, "opaque model")
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:  "cmf-raw-generate",
		Files:  map[string]string{"model.cmf": digest},
		Stream: &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	w = createRequest(t, s.GenerateHandler, api.GenerateRequest{
		Model:  "cmf-raw-generate",
		Prompt: "raw prompt",
		Raw:    true,
		Stream: &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("generate status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if mock.CompletionRequest.Prompt != "raw prompt" {
		t.Fatalf("completion prompt = %q, want raw prompt", mock.CompletionRequest.Prompt)
	}
}

func TestCMFGenerateRejectsUnsupportedInputsBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	mock := &mockRunner{
		CompletionFn: func(context.Context, llm.CompletionRequest, func(llm.CompletionResponse)) error {
			t.Fatal("invalid CMF request must not schedule Completion")
			return nil
		},
		ChatFn: func(context.Context, llm.ChatRequest, func(llm.ChatResponse)) error {
			t.Fatal("invalid CMF request must not schedule Chat")
			return nil
		},
	}
	s := newServerWithMockRunner(t, mock)
	var schedules atomic.Int32
	s.sched.loadFn = func(req *LlmRequest, _ ml.SystemInfo, _ []ml.DeviceInfo, _ bool) bool {
		schedules.Add(1)
		req.successCh <- &runnerRef{llama: mock}
		return false
	}
	digest := createCMFTestBlob(t, "opaque model")
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:  "cmf-reject-generate",
		Files:  map[string]string{"model.cmf": digest},
		Stream: &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	tests := []struct {
		name string
		req  api.GenerateRequest
	}{
		{name: "images", req: api.GenerateRequest{Model: "cmf-reject-generate", Prompt: "hello", Images: []api.ImageData{{1}}, Stream: &stream}},
		{name: "suffix", req: api.GenerateRequest{Model: "cmf-reject-generate", Prompt: "hello", Suffix: "suffix", Raw: true, Stream: &stream}},
		{name: "debug", req: api.GenerateRequest{Model: "cmf-reject-generate", Prompt: "hello", DebugRenderOnly: true, Raw: true, Stream: &stream}},
		{name: "format", req: api.GenerateRequest{Model: "cmf-reject-generate", Prompt: "hello", Format: json.RawMessage(`"json"`), Stream: &stream}},
		{name: "logprobs", req: api.GenerateRequest{Model: "cmf-reject-generate", Prompt: "hello", Logprobs: true, Stream: &stream}},
		{name: "top logprobs", req: api.GenerateRequest{Model: "cmf-reject-generate", Prompt: "hello", TopLogprobs: 1, Stream: &stream}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w = createRequest(t, s.GenerateHandler, tt.req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("request %#v status = %d, want %d: %s", tt.req, w.Code, http.StatusBadRequest, w.Body.String())
			}
			if got := schedules.Load(); got != 0 {
				t.Fatalf("CMF runner scheduled %d time(s), want 0", got)
			}
		})
	}
}

func TestCMFGenerateRejectsModelImageMessagesBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	mock := &mockRunner{}
	s := newServerWithMockRunner(t, mock)
	var schedules atomic.Int32
	s.sched.loadFn = func(req *LlmRequest, _ ml.SystemInfo, _ []ml.DeviceInfo, _ bool) bool {
		schedules.Add(1)
		req.successCh <- &runnerRef{llama: mock}
		return false
	}

	modelLayer, err := manifest.NewLayerFromLayer(
		createCMFTestBlob(t, "opaque model"), manifest.MediaTypeImageModel, "model.cmf",
	)
	if err != nil {
		t.Fatal(err)
	}
	messageJSON, err := json.Marshal([]api.Message{{
		Role:    "user",
		Content: "legacy image message",
		Images:  []api.ImageData{{1}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	messageLayer, err := manifest.NewLayer(
		strings.NewReader(string(messageJSON)), "application/vnd.ollama.image.messages",
	)
	if err != nil {
		t.Fatal(err)
	}
	writeShowManifestVariant(t, "cmf-model-image-message", manifest.RunnerCMF, manifest.FormatCMF,
		model.ConfigV2{
			ModelFormat:  manifest.FormatCMF,
			Capabilities: []string{string(model.CapabilityCompletion)},
		}, nil, modelLayer, messageLayer)

	w := createRequest(t, s.GenerateHandler, api.GenerateRequest{
		Model:  "cmf-model-image-message",
		Prompt: "hello",
		Stream: &stream,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "model messages containing images") {
		t.Fatalf("error = %s, want model-message image rejection", w.Body.String())
	}
	if got := schedules.Load(); got != 0 {
		t.Fatalf("CMF runner scheduled %d time(s), want 0", got)
	}
}

func TestCMFEmbeddingHandlersRejectBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	mock := &mockRunner{}
	s := newServerWithMockRunner(t, mock)
	var schedules atomic.Int32
	s.sched.loadFn = func(req *LlmRequest, _ ml.SystemInfo, _ []ml.DeviceInfo, _ bool) bool {
		schedules.Add(1)
		req.successCh <- &runnerRef{llama: mock}
		return false
	}
	digest := createCMFTestBlob(t, "opaque model")
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:  "cmf-no-embeddings",
		Files:  map[string]string{"model.cmf": digest},
		Stream: &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	tests := []struct {
		name    string
		handler func(*gin.Context)
		body    any
	}{
		{
			name:    "embed",
			handler: s.EmbedHandler,
			body:    api.EmbedRequest{Model: "cmf-no-embeddings", Input: "hello"},
		},
		{
			name:    "legacy embeddings",
			handler: s.EmbeddingsHandler,
			body:    api.EmbeddingRequest{Model: "cmf-no-embeddings", Prompt: "hello"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w = createRequest(t, tt.handler, tt.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusBadRequest, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "CMF models do not support embeddings") {
				t.Fatalf("error = %s, want CMF embedding rejection", w.Body.String())
			}
			if got := schedules.Load(); got != 0 {
				t.Fatalf("CMF runner scheduled %d time(s), want 0", got)
			}
		})
	}
}

func TestCMFChatRejectsUnsupportedOutputsBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OLLAMA_MODELS", t.TempDir())

	mock := &mockRunner{
		ChatFn: func(context.Context, llm.ChatRequest, func(llm.ChatResponse)) error {
			t.Fatal("invalid CMF request must not call Chat")
			return nil
		},
	}
	s := newServerWithMockRunner(t, mock)
	var schedules atomic.Int32
	s.sched.loadFn = func(req *LlmRequest, _ ml.SystemInfo, _ []ml.DeviceInfo, _ bool) bool {
		schedules.Add(1)
		req.successCh <- &runnerRef{llama: mock}
		return false
	}
	digest := createCMFTestBlob(t, "opaque model")
	w := createRequest(t, s.CreateHandler, api.CreateRequest{
		Model:  "cmf-reject-chat",
		Files:  map[string]string{"model.cmf": digest},
		Stream: &stream,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	tests := []struct {
		name string
		req  api.ChatRequest
	}{
		{name: "format", req: api.ChatRequest{Model: "cmf-reject-chat", Messages: []api.Message{{Role: "user", Content: "hello"}}, Format: json.RawMessage(`"json"`), Stream: &stream}},
		{name: "logprobs", req: api.ChatRequest{Model: "cmf-reject-chat", Messages: []api.Message{{Role: "user", Content: "hello"}}, Logprobs: true, Stream: &stream}},
		{name: "top logprobs", req: api.ChatRequest{Model: "cmf-reject-chat", Messages: []api.Message{{Role: "user", Content: "hello"}}, TopLogprobs: 1, Stream: &stream}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w = createRequest(t, s.ChatHandler, tt.req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("request %#v status = %d, want %d: %s", tt.req, w.Code, http.StatusBadRequest, w.Body.String())
			}
			if got := schedules.Load(); got != 0 {
				t.Fatalf("CMF runner scheduled %d time(s), want 0", got)
			}
		})
	}
}

func TestCMFOptionValidationReturnsBadRequest(t *testing.T) {
	m := &Model{Config: model.ConfigV2{ModelFormat: manifest.FormatCMF}}
	var s Server
	_, err := s.modelOptions(m, map[string]any{"temperature": "not-a-number"})
	var status api.StatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusBadRequest {
		t.Fatalf("modelOptions() error = %v, want HTTP 400 StatusError", err)
	}

	// Options Cortiq has no equivalent for are accepted and ignored, so
	// ordinary clients that always send them keep working.
	if _, err := s.modelOptions(m, map[string]any{"num_gpu": float64(0), "top_k": float64(40), "num_ctx": float64(8192)}); err != nil {
		t.Fatalf("modelOptions() with ignored options error = %v, want nil", err)
	}
}

func boolPointer(value bool) *bool { return &value }
