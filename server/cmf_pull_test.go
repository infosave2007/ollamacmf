package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/manifest"
	"github.com/ollama/ollama/types/model"
)

// fakeHuggingFace serves the model API and the LFS blob endpoint for one
// repository that publishes CMF files.
func fakeHuggingFace(t *testing.T, repo string, pipeline string, files map[string][]byte) (host string, blobRequests *atomic.Int32) {
	t.Helper()
	blobRequests = new(atomic.Int32)
	type sibling struct {
		Filename string `json:"rfilename"`
		Size     int64  `json:"size"`
		LFS      *struct {
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"lfs,omitempty"`
	}
	var siblings []sibling
	byDigest := make(map[string][]byte)
	for name, data := range files {
		sum := sha256.Sum256(data)
		digest := fmt.Sprintf("%x", sum)
		byDigest["sha256:"+digest] = data
		s := sibling{Filename: name, Size: int64(len(data))}
		s.LFS = &struct {
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		}{SHA256: digest, Size: int64(len(data))}
		siblings = append(siblings, s)
	}
	siblings = append(siblings, sibling{Filename: "README.md", Size: 12})

	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/models/"+repo:
			json.NewEncoder(w).Encode(map[string]any{
				"id":           repo,
				"pipeline_tag": pipeline,
				"siblings":     siblings,
			})
		case strings.HasPrefix(r.URL.Path, "/v2/"+repo+"/blobs/"):
			// Like Hugging Face, hand the transfer to a CDN-style URL.
			digest := strings.TrimPrefix(r.URL.Path, "/v2/"+repo+"/blobs/")
			data, ok := byDigest[digest]
			if !ok {
				http.NotFound(w, r)
				return
			}
			blobRequests.Add(1)
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			if r.Method == http.MethodGet {
				// The downloader reads the direct URL from Location and
				// transfers from there, so answer like the existing registry
				// tests do: 200 plus a Location header.
				w.Header().Set("Location", ts.URL+"/cdn/"+digest)
				w.WriteHeader(http.StatusOK)
			}
		case strings.HasPrefix(r.URL.Path, "/cdn/"):
			data, ok := byDigest[strings.TrimPrefix(r.URL.Path, "/cdn/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			http.ServeContent(w, r, "blob", time.Time{}, bytes.NewReader(data))
		case strings.HasPrefix(r.URL.Path, "/v2/"):
			http.Error(w, `{"error":"Repository is not GGUF or is not compatible with llama.cpp"}`, http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	host = strings.TrimPrefix(ts.URL, "http://")
	previousAPI := huggingFaceAPI
	previousHosts := huggingFaceHosts
	huggingFaceAPI = ts.URL
	huggingFaceHosts = append(slices.Clone(huggingFaceHosts), host)
	t.Cleanup(func() {
		huggingFaceAPI = previousAPI
		huggingFaceHosts = previousHosts
	})
	return host, blobRequests
}

func TestPullHuggingFaceCMFRegistersLocalModel(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	modelBytes, err := os.ReadFile(filepath.Join("..", "testdata", "cmf", "tiny-v0.8.19.cmf"))
	if err != nil {
		t.Fatal(err)
	}
	host, blobRequests := fakeHuggingFace(t, "infosave/tiny-cmf", "text-generation", map[string][]byte{
		"tiny-q4tp.cmf": modelBytes,
	})

	name := host + "/infosave/tiny-cmf"
	var statuses []string
	err = PullModel(t.Context(), name, "", &registryOptions{Insecure: true}, func(p api.ProgressResponse) {
		statuses = append(statuses, p.Status)
	})
	if err != nil {
		t.Fatalf("PullModel() error = %v", err)
	}
	if blobRequests.Load() == 0 {
		t.Fatal("the CMF file was not downloaded")
	}
	if statuses[len(statuses)-1] != "success" || !slices.Contains(statuses, "pulling manifest") {
		t.Fatalf("progress statuses = %v", statuses)
	}

	m, err := GetModel(name)
	if err != nil {
		t.Fatalf("GetModel() error = %v", err)
	}
	if !m.IsCMF() {
		t.Fatal("pulled model is not CMF")
	}
	if m.Config.ModelFamily != "tiny-qwen" || m.Config.ContextLen != 128 || m.Config.ModelType == "" {
		t.Fatalf("config = %+v, want header metadata", m.Config)
	}
	mf, err := manifest.ParseNamedManifest(model.ParseName(name))
	if err != nil {
		t.Fatal(err)
	}
	if mf.Runner != manifest.RunnerCMF || mf.Format != manifest.FormatCMF {
		t.Fatalf("manifest runner/format = %q/%q", mf.Runner, mf.Format)
	}

	// Pulling again reuses the blob.
	before := blobRequests.Load()
	if err := PullModel(t.Context(), name, "", &registryOptions{Insecure: true}, func(api.ProgressResponse) {}); err != nil {
		t.Fatalf("second PullModel() error = %v", err)
	}
	if got := blobRequests.Load(); got != before {
		t.Fatalf("second pull downloaded the blob again (%d requests)", got-before)
	}
}

func TestPullHuggingFaceCMFSelectsFileByTag(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	modelBytes, err := os.ReadFile(filepath.Join("..", "testdata", "cmf", "tiny-v0.8.19.cmf"))
	if err != nil {
		t.Fatal(err)
	}
	other := append(slices.Clone(modelBytes), 0) // distinct digest, still a valid container
	host, _ := fakeHuggingFace(t, "infosave/multi-cmf", "text-generation", map[string][]byte{
		"model-3b-q4tp.cmf":          modelBytes,
		"model-8b-q4tp.cmf":          other,
		"model-8b-q4tp.mtp.cmf":      other, // draft head, never a default or substring match
		"experiments/tool.skill.cmf": other,
	})
	ignore := func(api.ProgressResponse) {}

	err = PullModel(t.Context(), host+"/infosave/multi-cmf", "", &registryOptions{Insecure: true}, ignore)
	if !errors.Is(err, errCMFPullAmbiguous) || !strings.Contains(err.Error(), "model-3b-q4tp") {
		t.Fatalf("default tag error = %v, want an ambiguity error listing the files", err)
	}

	err = PullModel(t.Context(), host+"/infosave/multi-cmf:8b-q4tp", "", &registryOptions{Insecure: true}, ignore)
	if err != nil {
		t.Fatalf("substring tag PullModel() error = %v", err)
	}
	if _, err := GetModel(host + "/infosave/multi-cmf:8b-q4tp"); err != nil {
		t.Fatalf("model selected by tag is missing: %v", err)
	}

	err = PullModel(t.Context(), host+"/infosave/multi-cmf:q4tp", "", &registryOptions{Insecure: true}, ignore)
	if err == nil || !strings.Contains(err.Error(), "matches several") {
		t.Fatalf("ambiguous substring error = %v", err)
	}

	err = PullModel(t.Context(), host+"/infosave/multi-cmf:nope", "", &registryOptions{Insecure: true}, ignore)
	if !errors.Is(err, errCMFPullNotFound) {
		t.Fatalf("unknown tag error = %v, want errCMFPullNotFound", err)
	}
}

func TestPullHuggingFaceCMFRejectsNonTextModels(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	host, blobRequests := fakeHuggingFace(t, "infosave/whisper-cmf", "automatic-speech-recognition", map[string][]byte{
		"whisper.cmf": []byte("not downloaded"),
	})
	err := PullModel(t.Context(), host+"/infosave/whisper-cmf", "", &registryOptions{Insecure: true}, func(api.ProgressResponse) {})
	if err == nil || !strings.Contains(err.Error(), "automatic-speech-recognition") {
		t.Fatalf("PullModel() error = %v, want a pipeline rejection", err)
	}
	if blobRequests.Load() != 0 {
		t.Fatal("a rejected model was downloaded")
	}
}

func TestPullHuggingFaceWithoutCMFFallsThrough(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", t.TempDir())
	host, _ := fakeHuggingFace(t, "someone/gguf-only", "text-generation", map[string][]byte{})
	err := PullModel(t.Context(), host+"/someone/gguf-only", "", &registryOptions{Insecure: true}, func(api.ProgressResponse) {})
	if err == nil || !strings.Contains(err.Error(), "pull model manifest") {
		t.Fatalf("PullModel() error = %v, want the ordinary manifest error", err)
	}
}
