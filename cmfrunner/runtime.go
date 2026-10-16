package cmfrunner

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
)

// Environment variables that control where the Cortiq runtime comes from.
const (
	// EnvCortiqPath points at a specific cortiq executable. When set it is
	// used as-is and nothing else is consulted.
	EnvCortiqPath = "OLLAMA_CORTIQ_PATH"
	// EnvCortiqDir is the directory managed runtimes are installed into.
	// Default: a "cortiq" directory next to the models directory.
	EnvCortiqDir = "OLLAMA_CORTIQ_DIR"
	// EnvCortiqAutoInstall set to 0/false disables downloading the pinned
	// runtime automatically.
	EnvCortiqAutoInstall = "OLLAMA_CORTIQ_AUTO_INSTALL"
)

var (
	// ErrRuntimeUnsupportedPlatform means the pinned Cortiq release has no
	// build for this OS/architecture.
	ErrRuntimeUnsupportedPlatform = errors.New("CMF runtime is not available for this platform")
	// ErrRuntimeAutoInstallDisabled means no runtime was found and automatic
	// installation is switched off.
	ErrRuntimeAutoInstallDisabled = errors.New("CMF runtime is not installed and automatic installation is disabled")

	installMu sync.Mutex
)

// RuntimeSource says where a Cortiq executable was found.
type RuntimeSource string

const (
	SourceEnv     RuntimeSource = "OLLAMA_CORTIQ_PATH"
	SourceManaged RuntimeSource = "managed"
	SourceBundled RuntimeSource = "bundled"
	SourcePATH    RuntimeSource = "PATH"
	SourceNone    RuntimeSource = ""
)

// RuntimeStatus describes the Cortiq runtime as the server sees it.
type RuntimeStatus struct {
	// Installed is true when a usable executable was found.
	Installed bool `json:"installed"`
	// Path is the executable that will be used, if any.
	Path string `json:"path,omitempty"`
	// Source says where Path came from.
	Source RuntimeSource `json:"source,omitempty"`
	// Version is the executable's reported version, when it could be probed.
	Version string `json:"version,omitempty"`
	// PinnedVersion is the Cortiq release this build was tested with.
	PinnedVersion string `json:"pinned_version"`
	// Platform is the Go platform of this process.
	Platform string `json:"platform"`
	// PlatformSupported is false when the pinned release has no build for
	// Platform. Error explains why.
	PlatformSupported bool `json:"platform_supported"`
	// InstallDir is where managed runtimes live.
	InstallDir string `json:"install_dir"`
	// AutoInstall reports whether a missing runtime is downloaded on demand.
	AutoInstall bool   `json:"auto_install"`
	Error       string `json:"error,omitempty"`
}

// Outdated reports whether the discovered runtime differs from the pinned one.
func (s RuntimeStatus) Outdated() bool {
	return s.Installed && s.Version != "" && s.PinnedVersion != "" &&
		strings.TrimPrefix(s.Version, "v") != strings.TrimPrefix(s.PinnedVersion, "v")
}

// InstallDir returns the directory managed Cortiq runtimes are installed in.
func InstallDir() string {
	if dir := strings.TrimSpace(os.Getenv(EnvCortiqDir)); dir != "" {
		return dir
	}
	return filepath.Join(filepath.Dir(envconfig.Models()), "cortiq")
}

// AutoInstallEnabled reports whether a missing runtime may be downloaded.
func AutoInstallEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvCortiqAutoInstall))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// ManagedExecutable is where the pinned runtime is installed by Install.
func ManagedExecutable(lock *Lock) string {
	return filepath.Join(InstallDir(), lock.Version(), cortiqBinaryName(runtime.GOOS))
}

// FindCortiq locates a Cortiq executable without installing one.
//
// Lookup order: OLLAMA_CORTIQ_PATH, the managed install of the pinned
// release, a cortiq binary next to the ollamacmf executable (release
// archives ship one), then PATH.
func FindCortiq() (string, RuntimeSource, error) {
	lock, err := LoadLock()
	if err != nil {
		return "", SourceNone, err
	}
	return findCortiq(lock, os.Getenv(EnvCortiqPath), os.Getenv("PATH"), executableDir())
}

func findCortiq(lock *Lock, explicit, pathEnv, exeDir string) (string, RuntimeSource, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", SourceNone, fmt.Errorf("%w: %s must be an absolute path, got %q", ErrRuntimeUnavailable, EnvCortiqPath, explicit)
		}
		if !executableFile(explicit) {
			return "", SourceNone, fmt.Errorf("%w: %s=%q is not an executable file", ErrRuntimeUnavailable, EnvCortiqPath, explicit)
		}
		return explicit, SourceEnv, nil
	}
	if managed := ManagedExecutable(lock); executableFile(managed) {
		return managed, SourceManaged, nil
	}
	name := cortiqBinaryName(runtime.GOOS)
	if exeDir != "" {
		for _, candidate := range []string{
			filepath.Join(exeDir, name),
			filepath.Join(exeDir, "lib", "ollama", name),
			filepath.Join(exeDir, "..", "lib", "ollama", name),
		} {
			if executableFile(candidate) {
				return filepath.Clean(candidate), SourceBundled, nil
			}
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		if candidate := filepath.Join(dir, name); executableFile(candidate) {
			return candidate, SourcePATH, nil
		}
	}
	return "", SourceNone, fmt.Errorf("%w: run `ollamacmf cmf setup`, or set %s", ErrRuntimeUnavailable, EnvCortiqPath)
}

// EnsureRuntime returns a Cortiq executable, installing the pinned release
// when none is present and automatic installation is enabled. Progress is
// reported through fn, which may be nil.
func EnsureRuntime(ctx context.Context, fn func(api.ProgressResponse)) (string, error) {
	lock, err := LoadLock()
	if err != nil {
		return "", err
	}
	path, source, err := FindCortiq()
	if err == nil {
		if source == SourcePATH || source == SourceEnv {
			if version := probeVersion(ctx, path); version != "" && !sameVersion(version, lock.Version()) {
				slog.Warn("Cortiq runtime version differs from the release ollamacmf was tested with",
					"path", path, "version", version, "pinned", lock.Version())
			}
		}
		return path, nil
	}
	if !AutoInstallEnabled() {
		return "", fmt.Errorf("%w: run `ollamacmf cmf setup` or set %s", ErrRuntimeAutoInstallDisabled, EnvCortiqPath)
	}
	if _, err := lock.ArtifactFor(CurrentPlatform()); err != nil {
		return "", fmt.Errorf("%w; build Cortiq from source (cargo install cortiq-cli) and set %s", err, EnvCortiqPath)
	}
	slog.Info("CMF runtime not found, downloading the pinned Cortiq release", "version", lock.Version(), "dir", InstallDir())
	return Install(ctx, fn)
}

// Install downloads, verifies and installs the pinned Cortiq release for this
// platform into InstallDir. It is safe to call when the runtime is already
// installed; it then returns immediately.
func Install(ctx context.Context, fn func(api.ProgressResponse)) (string, error) {
	lock, err := LoadLock()
	if err != nil {
		return "", err
	}
	artifact, err := lock.ArtifactFor(CurrentPlatform())
	if err != nil {
		return "", err
	}
	return install(ctx, lock, artifact, artifact.Archive.URL, fn)
}

func install(ctx context.Context, lock *Lock, artifact *Artifact, downloadURL string, fn func(api.ProgressResponse)) (string, error) {
	if fn == nil {
		fn = func(api.ProgressResponse) {}
	}
	installMu.Lock()
	defer installMu.Unlock()

	target := ManagedExecutable(lock)
	if executableFile(target) {
		fn(api.ProgressResponse{Status: fmt.Sprintf("Cortiq %s is already installed", lock.Version())})
		return target, nil
	}

	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create runtime directory: %w", err)
	}
	archivePath := filepath.Join(dir, artifact.Archive.Name+".partial")
	defer os.Remove(archivePath)

	fn(api.ProgressResponse{Status: fmt.Sprintf("downloading Cortiq %s", lock.Version()), Total: artifact.Archive.SizeBytes})
	if err := downloadFile(ctx, downloadURL, archivePath, artifact.Archive.SizeBytes, fn); err != nil {
		return "", fmt.Errorf("download %s: %w", artifact.Archive.Name, err)
	}

	fn(api.ProgressResponse{Status: "verifying checksum"})
	if err := verifySHA256(archivePath, artifact.Archive.SHA256, artifact.Archive.SizeBytes); err != nil {
		return "", err
	}

	fn(api.ProgressResponse{Status: "installing runtime"})
	entry := artifact.InstalledExecutable
	if len(artifact.Archive.Entries) == 1 {
		entry = artifact.Archive.Entries[0]
	}
	tmp := target + ".partial"
	defer os.Remove(tmp)
	if err := extractEntry(archivePath, artifact.Archive.Format, entry, tmp); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		if executableFile(target) {
			// Another process won the race; its copy was verified the same way.
			return target, nil
		}
		return "", err
	}
	slog.Info("installed Cortiq runtime", "version", lock.Version(), "path", target)
	fn(api.ProgressResponse{Status: "success"})
	return target, nil
}

// Status reports the runtime the server would use for the next CMF model.
func Status(ctx context.Context) RuntimeStatus {
	status := RuntimeStatus{
		Platform:    CurrentPlatform(),
		InstallDir:  InstallDir(),
		AutoInstall: AutoInstallEnabled(),
	}
	lock, err := LoadLock()
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.PinnedVersion = lock.Version()
	if _, err := lock.ArtifactFor(CurrentPlatform()); err == nil {
		status.PlatformSupported = true
	} else {
		status.Error = err.Error()
	}

	path, source, err := FindCortiq()
	if err != nil {
		if status.Error == "" {
			status.Error = err.Error()
		}
		return status
	}
	status.Installed = true
	status.Path = path
	status.Source = source
	status.Version = probeVersion(ctx, path)
	if status.Outdated() {
		status.Error = fmt.Sprintf("runtime is Cortiq %s but this build expects %s; run `ollamacmf cmf setup`", status.Version, status.PinnedVersion)
	}
	return status
}

func probeVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func sameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	// Windows does not preserve Unix executable bits for packaged binaries.
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

func cortiqBinaryName(goos string) string {
	if goos == "windows" {
		return "cortiq.exe"
	}
	return "cortiq"
}

func downloadFile(ctx context.Context, url, dest string, expected int64, fn func(api.ProgressResponse)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ollamacmf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	status := fmt.Sprintf("downloading %s", filepath.Base(url))
	var completed int64
	lastReport := time.Now()
	buf := make([]byte, 1<<20)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				return err
			}
			completed += int64(n)
			if time.Since(lastReport) > 200*time.Millisecond {
				fn(api.ProgressResponse{Status: status, Total: expected, Completed: completed})
				lastReport = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			return readErr
		}
	}
	fn(api.ProgressResponse{Status: status, Total: expected, Completed: completed})
	return f.Close()
}

func verifySHA256(path, want string, wantSize int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if wantSize > 0 && info.Size() != wantSize {
		return fmt.Errorf("downloaded archive is %d bytes, expected %d", info.Size(), wantSize)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("downloaded archive checksum %s does not match the pinned %s", got, want)
	}
	return nil
}

// extractEntry copies exactly one named file out of a tar.gz or zip archive.
func extractEntry(archivePath, format, entry, dest string) error {
	switch format {
	case "tar.gz", "tgz":
		return extractTarGzEntry(archivePath, entry, dest)
	case "zip":
		return extractZipEntry(archivePath, entry, dest)
	default:
		return fmt.Errorf("unsupported runtime archive format %q", format)
	}
}

func extractTarGzEntry(archivePath, entry, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || !entryMatches(hdr.Name, entry) {
			continue
		}
		return writeEntry(dest, tr, hdr.Size)
	}
	return fmt.Errorf("archive does not contain %s", entry)
}

func extractZipEntry(archivePath, entry, dest string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, file := range zr.File {
		if file.FileInfo().IsDir() || !entryMatches(file.Name, entry) {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return writeEntry(dest, rc, int64(file.UncompressedSize64))
	}
	return fmt.Errorf("archive does not contain %s", entry)
}

func entryMatches(name, entry string) bool {
	name = strings.TrimPrefix(strings.ReplaceAll(name, "\\", "/"), "./")
	return name == entry || strings.HasSuffix(name, "/"+entry)
}

func writeEntry(dest string, r io.Reader, size int64) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	limit := size
	if limit <= 0 {
		limit = 1 << 30
	}
	if _, err := io.Copy(out, io.LimitReader(r, limit+1)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
