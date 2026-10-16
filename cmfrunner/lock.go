package cmfrunner

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
)

// lockJSON is the pinned Cortiq release this build of ollamacmf runs CMF
// models with. The file is the single source of truth for the runtime
// version, download URLs and checksums; the release workflow and the
// automatic runtime installer both read it.
//
//go:embed cmf.lock.json
var lockJSON []byte

// Lock describes the pinned Cortiq runtime release.
type Lock struct {
	SchemaVersion int `json:"schemaVersion"`
	Upstream      struct {
		Repository string `json:"repository"`
		License    string `json:"license"`
		Release    struct {
			Tag string `json:"tag"`
			URL string `json:"url"`
		} `json:"release"`
	} `json:"upstream"`
	Artifacts            []Artifact `json:"artifacts"`
	UnsupportedPlatforms []struct {
		Platform string `json:"platform"`
		Reason   string `json:"reason"`
	} `json:"unsupportedPlatforms"`
}

// Artifact is one downloadable Cortiq build.
type Artifact struct {
	Platform string `json:"platform"`
	Archive  struct {
		Name      string   `json:"name"`
		URL       string   `json:"url"`
		SizeBytes int64    `json:"sizeBytes"`
		SHA256    string   `json:"sha256"`
		Format    string   `json:"format"`
		Entries   []string `json:"entries"`
	} `json:"archive"`
	InstalledExecutable string `json:"installedExecutable"`
}

var (
	lockOnce   sync.Once
	lockParsed Lock
	lockErr    error
)

// LoadLock returns the embedded runtime lock.
func LoadLock() (*Lock, error) {
	lockOnce.Do(func() {
		lockErr = json.Unmarshal(lockJSON, &lockParsed)
	})
	if lockErr != nil {
		return nil, fmt.Errorf("parse embedded CMF runtime lock: %w", lockErr)
	}
	return &lockParsed, nil
}

// Version returns the pinned Cortiq release tag, for example "v0.8.19".
func (l *Lock) Version() string {
	return l.Upstream.Release.Tag
}

// ArtifactFor returns the artifact for a Go platform such as "darwin/arm64".
func (l *Lock) ArtifactFor(platform string) (*Artifact, error) {
	for i := range l.Artifacts {
		if l.Artifacts[i].Platform == platform {
			return &l.Artifacts[i], nil
		}
	}
	for _, unsupported := range l.UnsupportedPlatforms {
		if unsupported.Platform == platform {
			return nil, fmt.Errorf("%w: %s", ErrRuntimeUnsupportedPlatform, unsupported.Reason)
		}
	}
	return nil, fmt.Errorf("%w: no Cortiq %s build for %s", ErrRuntimeUnsupportedPlatform, l.Version(), platform)
}

// CurrentPlatform is the Go platform string of this process.
func CurrentPlatform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}
