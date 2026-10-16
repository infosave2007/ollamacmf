package cmfrunner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

func TestLockDescribesCurrentPlatform(t *testing.T) {
	lock, err := LoadLock()
	if err != nil {
		t.Fatalf("LoadLock() error = %v", err)
	}
	if !strings.HasPrefix(lock.Version(), "v") {
		t.Fatalf("lock version = %q, want a vX.Y.Z tag", lock.Version())
	}
	for _, platform := range []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "windows/amd64", "windows/arm64"} {
		artifact, err := lock.ArtifactFor(platform)
		if err != nil {
			t.Fatalf("ArtifactFor(%s) error = %v", platform, err)
		}
		if artifact.Archive.URL == "" || artifact.Archive.SHA256 == "" || artifact.Archive.SizeBytes == 0 {
			t.Fatalf("ArtifactFor(%s) is incomplete: %+v", platform, artifact)
		}
	}
	if _, err := lock.ArtifactFor("plan9/mips"); !errors.Is(err, ErrRuntimeUnsupportedPlatform) {
		t.Fatalf("ArtifactFor(plan9/mips) error = %v, want ErrRuntimeUnsupportedPlatform", err)
	}
}

func TestFindCortiqLookupOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable bits are not meaningful on Windows")
	}
	lock, err := LoadLock()
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	t.Setenv(EnvCortiqDir, filepath.Join(temp, "managed"))

	explicit := writeExecutable(t, filepath.Join(temp, "explicit", "cortiq"))
	managed := writeExecutable(t, ManagedExecutable(lock))
	bundled := writeExecutable(t, filepath.Join(temp, "bundle", "cortiq"))
	onPath := writeExecutable(t, filepath.Join(temp, "path", "cortiq"))

	got, source, err := findCortiq(lock, explicit, filepath.Dir(onPath), filepath.Dir(bundled))
	if err != nil || got != explicit || source != SourceEnv {
		t.Fatalf("explicit lookup = %q (%s), %v; want %q", got, source, err, explicit)
	}
	if _, _, err := findCortiq(lock, "cortiq", "", ""); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("relative explicit path error = %v, want ErrRuntimeUnavailable", err)
	}

	got, source, err = findCortiq(lock, "", filepath.Dir(onPath), filepath.Dir(bundled))
	if err != nil || got != managed || source != SourceManaged {
		t.Fatalf("managed lookup = %q (%s), %v; want %q", got, source, err, managed)
	}
	os.Remove(managed)

	got, source, err = findCortiq(lock, "", filepath.Dir(onPath), filepath.Dir(bundled))
	if err != nil || got != bundled || source != SourceBundled {
		t.Fatalf("bundled lookup = %q (%s), %v; want %q", got, source, err, bundled)
	}
	os.Remove(bundled)

	got, source, err = findCortiq(lock, "", filepath.Dir(onPath), filepath.Dir(bundled))
	if err != nil || got != onPath || source != SourcePATH {
		t.Fatalf("PATH lookup = %q (%s), %v; want %q", got, source, err, onPath)
	}
	os.Remove(onPath)

	if _, _, err := findCortiq(lock, "", filepath.Dir(onPath), filepath.Dir(bundled)); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("empty lookup error = %v, want ErrRuntimeUnavailable", err)
	}
}

func TestInstallDownloadsVerifiesAndExtracts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release is a tar.gz with a Unix executable")
	}
	t.Setenv(EnvCortiqDir, t.TempDir())
	lock, err := LoadLock()
	if err != nil {
		t.Fatal(err)
	}

	script := []byte("#!/bin/sh\necho cortiq 9.9.9\n")
	archive := tarGz(t, map[string][]byte{"cortiq": script, "README": []byte("not extracted")})
	sum := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer server.Close()

	artifact := &Artifact{Platform: CurrentPlatform(), InstalledExecutable: "cortiq"}
	artifact.Archive.Name = "cortiq-test.tar.gz"
	artifact.Archive.Format = "tar.gz"
	artifact.Archive.Entries = []string{"cortiq"}
	artifact.Archive.SizeBytes = int64(len(archive))
	artifact.Archive.SHA256 = hex.EncodeToString(sum[:])

	var statuses []string
	path, err := install(t.Context(), lock, artifact, server.URL, func(p api.ProgressResponse) { statuses = append(statuses, p.Status) })
	if err != nil {
		t.Fatalf("install() error = %v", err)
	}
	if path != ManagedExecutable(lock) {
		t.Fatalf("install() = %q, want %q", path, ManagedExecutable(lock))
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, script) {
		t.Fatalf("installed file = %q, %v; want the archive entry", got, err)
	}
	if !executableFile(path) {
		t.Fatal("installed runtime is not executable")
	}
	if len(statuses) == 0 || statuses[len(statuses)-1] != "success" {
		t.Fatalf("progress statuses = %v, want a trailing success", statuses)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "README")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("install extracted more than the executable entry")
	}

	// A second call is a no-op.
	if again, err := install(t.Context(), lock, artifact, "http://127.0.0.1:1/unused", nil); err != nil || again != path {
		t.Fatalf("second install() = %q, %v; want the existing runtime", again, err)
	}

	// A corrupted download is rejected and nothing is installed.
	os.Remove(path)
	artifact.Archive.SHA256 = strings.Repeat("0", 64)
	if _, err := install(t.Context(), lock, artifact, server.URL, nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("install() with a wrong checksum error = %v, want a checksum error", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a runtime with a wrong checksum was installed")
	}

	got, err := EnsureRuntimeStatusForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	if got.Installed {
		t.Fatalf("status reports an installed runtime after removal: %+v", got)
	}
}

// EnsureRuntimeStatusForTest isolates Status from the host's PATH.
func EnsureRuntimeStatusForTest(t *testing.T) (RuntimeStatus, error) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv(EnvCortiqPath, "")
	return Status(context.Background()), nil
}

func TestReadInfoFromFixture(t *testing.T) {
	info, err := ReadInfoFile(filepath.Join("..", "testdata", "cmf", "tiny-v0.8.19.cmf"))
	if err != nil {
		t.Fatalf("ReadInfoFile() error = %v", err)
	}
	if info.Architecture() != "tiny-qwen" {
		t.Fatalf("architecture = %q, want tiny-qwen", info.Architecture())
	}
	if info.ContextLength() != 128 {
		t.Fatalf("context length = %d, want 128", info.ContextLength())
	}
	if info.ParameterCount == 0 {
		t.Fatal("parameter count was not read from the tensor directory")
	}
	if info.Kind() != "text" {
		t.Fatalf("kind = %q, want text", info.Kind())
	}
	if err := info.Runnable(); err != nil {
		t.Fatalf("Runnable() error = %v", err)
	}
}

func TestParseEnvelopeRejectsNonRunnableContainers(t *testing.T) {
	raw := make([]byte, cmfEnvelopeLength)
	copy(raw, cmfMagic)
	raw[4] = cmfVersion
	raw[0x10] = cmfEnvelopeLength
	raw[0x18] = 1 // header length
	raw[0x20] = cmfEnvelopeLength + 1
	raw[0x28] = 1 // directory length

	if _, err := ParseEnvelope(raw, 4096); err != nil {
		t.Fatalf("minimal envelope error = %v", err)
	}
	raw[12] = 1 << 3 // reserved DELTA_MASKS
	if _, err := ParseEnvelope(raw, 4096); !errors.Is(err, ErrInvalidCMF) {
		t.Fatalf("unknown feature error = %v, want ErrInvalidCMF", err)
	}
	raw[12] = 0
	raw[4] = 1
	if _, err := ParseEnvelope(raw, 4096); !errors.Is(err, ErrInvalidCMF) {
		t.Fatalf("version 1 error = %v, want ErrInvalidCMF", err)
	}

	skill := &Info{}
	skill.Envelope.RequiredFeatures = cmfSkillFileFeature
	if err := skill.Runnable(); err == nil || !strings.Contains(err.Error(), "skill") {
		t.Fatalf("skill Runnable() = %v, want a skill error", err)
	}
	image := &Info{}
	image.Header.Arch.Name = "lumina2-image"
	image.Header.Arch.NumLayers = 1
	image.Header.Arch.VocabSize = 1
	if err := image.Runnable(); err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("image Runnable() = %v, want an image error", err)
	}
}

func TestInfoCapabilitiesFromChatTemplate(t *testing.T) {
	info := &Info{}
	info.Header.TokenizerConfig.ChatTemplate = "{%- if tools %}{{ tools }}{%- endif %}{%- if enable_thinking %}<think>{%- endif %}"
	if !info.SupportsTools() || !info.SupportsThinking() {
		t.Fatalf("tools=%v thinking=%v, want both true", info.SupportsTools(), info.SupportsThinking())
	}
	info.Header.TokenizerConfig.ChatTemplate = "{{ messages }}"
	if info.SupportsTools() || info.SupportsThinking() {
		t.Fatal("plain template must not advertise tools or thinking")
	}
}

func TestMaxTokensDefaultsToModelContext(t *testing.T) {
	if got := cmfMaxTokens(-1, 0); got != cmfDefaultMaxTokens {
		t.Fatalf("cmfMaxTokens(-1, 0) = %d, want %d", got, cmfDefaultMaxTokens)
	}
	if got := cmfMaxTokens(-1, 4096); got != 4096 {
		t.Fatalf("cmfMaxTokens(-1, 4096) = %d, want 4096", got)
	}
	if got := cmfMaxTokens(-2, 1<<20); got != cmfDefaultMaxTokens {
		t.Fatalf("cmfMaxTokens(-2, large) = %d, want %d", got, cmfDefaultMaxTokens)
	}
	if got := cmfMaxTokens(12, 4096); got != 12 {
		t.Fatalf("cmfMaxTokens(12, 4096) = %d, want 12", got)
	}
}

func TestStopFilterCutsAcrossChunks(t *testing.T) {
	filter := newStopFilter([]string{"<END>", "###"})
	var out strings.Builder
	chunks := []string{"Hello, wo", "rld<E", "ND> hidden"}
	stopped := false
	for _, chunk := range chunks {
		text, hit := filter.push(chunk)
		out.WriteString(text)
		if hit {
			stopped = true
			break
		}
	}
	if !stopped || out.String() != "Hello, world" {
		t.Fatalf("stop filter output = %q (stopped=%v), want %q", out.String(), stopped, "Hello, world")
	}

	filter = newStopFilter([]string{"###"})
	out.Reset()
	for _, chunk := range []string{"no sto", "p here"} {
		text, hit := filter.push(chunk)
		out.WriteString(text)
		if hit {
			t.Fatal("unexpected stop")
		}
	}
	out.WriteString(filter.flush())
	if out.String() != "no stop here" {
		t.Fatalf("stop filter output = %q, want the whole text", out.String())
	}

	filter = newStopFilter(nil)
	if text, hit := filter.push("passthrough"); text != "passthrough" || hit {
		t.Fatalf("filter without stops = %q, %v", text, hit)
	}
}

func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
