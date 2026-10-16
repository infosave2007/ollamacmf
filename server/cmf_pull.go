package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/manifest"
	"github.com/ollama/ollama/types/model"
)

var (
	errCMFPullNotFound  = errors.New("no CMF file matches the requested tag")
	errCMFPullAmbiguous = errors.New("the repository has several CMF files")

	// huggingFaceAPI is overridden by tests.
	huggingFaceAPI = "https://huggingface.co"
)

// huggingFaceHosts are the hosts a model name may use to refer to Hugging
// Face. Tests append their fake server here.
var huggingFaceHosts = []string{"hf.co", "huggingface.co", "www.huggingface.co"}

// isHuggingFaceHost reports whether a model name points at Hugging Face.
func isHuggingFaceHost(host string) bool {
	return slices.Contains(huggingFaceHosts, strings.ToLower(host))
}

// huggingFaceFile is one file in a Hugging Face repository.
type huggingFaceFile struct {
	Path   string
	Size   int64
	SHA256 string
}

// Tag is the short name a user types after the colon to select this file:
// the base name without the .cmf extension.
func (f huggingFaceFile) Tag() string {
	return strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path))
}

// huggingFaceRepo is the subset of the Hugging Face model API ollamacmf uses.
type huggingFaceRepo struct {
	ID          string
	PipelineTag string
	Private     bool
	Gated       bool
	Files       []huggingFaceFile
}

// CMFFiles returns the repository's .cmf files, most relevant first.
func (r *huggingFaceRepo) CMFFiles() []huggingFaceFile {
	var files []huggingFaceFile
	for _, f := range r.Files {
		if strings.EqualFold(path.Ext(f.Path), ".cmf") {
			files = append(files, f)
		}
	}
	slices.SortFunc(files, func(a, b huggingFaceFile) int {
		// Top-level files before files in subdirectories, then by name.
		if da, db := strings.Count(a.Path, "/"), strings.Count(b.Path, "/"); da != db {
			return da - db
		}
		return strings.Compare(a.Path, b.Path)
	})
	return files
}

// pullHuggingFaceCMF downloads a .cmf file from a Hugging Face repository and
// registers it as a local model. It returns handled=false when the
// repository has no CMF files, so the ordinary (GGUF) pull can proceed.
func pullHuggingFaceCMF(ctx context.Context, n model.Name, regOpts *registryOptions, fn func(api.ProgressResponse)) (handled bool, err error) {
	if regOpts == nil {
		regOpts = &registryOptions{}
	}
	if regOpts.Token == "" {
		regOpts.Token = strings.TrimSpace(os.Getenv("HF_TOKEN"))
	}

	repo, err := fetchHuggingFaceRepo(ctx, n, regOpts)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return true, err
	}
	files := repo.CMFFiles()
	if len(files) == 0 {
		return false, nil
	}

	if err := checkHuggingFacePipeline(n, repo.PipelineTag); err != nil {
		return true, err
	}
	file, err := selectHuggingFaceCMF(n, files)
	if err != nil {
		return true, err
	}
	if file.SHA256 == "" {
		return true, fmt.Errorf("%s is not stored with Git LFS on Hugging Face, so its checksum is unknown", file.Path)
	}

	fn(api.ProgressResponse{Status: "pulling manifest"})
	digest := "sha256:" + strings.ToLower(file.SHA256)
	cacheHit, err := downloadBlob(ctx, downloadOpts{n: n, digest: digest, regOpts: regOpts, fn: fn})
	if err != nil {
		return true, err
	}
	if !cacheHit {
		fn(api.ProgressResponse{Status: "verifying sha256 digest"})
		if err := verifyBlob(digest); err != nil {
			if errors.Is(err, errDigestMismatch) {
				if fp, pathErr := manifest.BlobsPath(digest); pathErr == nil {
					os.Remove(fp)
				}
			}
			return true, err
		}
	}

	fn(api.ProgressResponse{Status: "validating CMF"})
	info, err := readCMFInfo(digest)
	if err != nil {
		return true, err
	}
	if err := info.Runnable(); err != nil {
		return true, err
	}
	layer, err := manifest.NewLayerFromLayer(digest, manifest.MediaTypeImageModel, path.Base(file.Path))
	if err != nil {
		return true, err
	}

	var config model.ConfigV2
	if err := createModel(ctx, api.CreateRequest{}, n, []*modelLayer{{Layer: layer, CMF: true, CMFInfo: info}}, &config, fn); err != nil {
		return true, err
	}
	fn(api.ProgressResponse{Status: "success"})
	return true, nil
}

// selectHuggingFaceCMF picks the file the name's tag refers to. The default
// tag selects the only file; otherwise the tag must name a file by its base
// name, or be a unique substring of one.
func selectHuggingFaceCMF(n model.Name, files []huggingFaceFile) (huggingFaceFile, error) {
	// Companion files (skill overlays, MTP drafts, projectors) are not
	// standalone models; never pick them by default.
	candidates := slices.DeleteFunc(slices.Clone(files), func(f huggingFaceFile) bool {
		return isCompanionCMF(f.Path)
	})
	if len(candidates) == 0 {
		candidates = files
	}

	tag := strings.ToLower(strings.TrimSpace(n.Tag))
	if tag == "" || tag == "latest" {
		if len(candidates) == 1 {
			return candidates[0], nil
		}
		return huggingFaceFile{}, fmt.Errorf("%w; choose one with a tag, for example:\n%s", errCMFPullAmbiguous, formatHuggingFaceChoices(n, candidates))
	}

	var exact, partial []huggingFaceFile
	for _, f := range files {
		candidate := strings.ToLower(f.Tag())
		switch {
		case candidate == tag:
			exact = append(exact, f)
		case strings.Contains(candidate, tag) && !isCompanionCMF(f.Path):
			partial = append(partial, f)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) == 0 && len(partial) == 1:
		return partial[0], nil
	case len(exact) > 1 || len(partial) > 1:
		matches := exact
		if len(matches) == 0 {
			matches = partial
		}
		return huggingFaceFile{}, fmt.Errorf("tag %q matches several CMF files; use one of:\n%s", n.Tag, formatHuggingFaceChoices(n, matches))
	default:
		return huggingFaceFile{}, fmt.Errorf("%w %q; available:\n%s", errCMFPullNotFound, n.Tag, formatHuggingFaceChoices(n, files))
	}
}

// isCompanionCMF reports whether a file accompanies a model rather than
// being one: skill overlays, MTP draft heads and multimodal projectors.
func isCompanionCMF(name string) bool {
	name = strings.ToLower(name)
	for _, suffix := range []string{".skill.cmf", ".mtp.cmf", ".mm.cmf"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func formatHuggingFaceChoices(n model.Name, files []huggingFaceFile) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "  ollamacmf pull %s/%s/%s:%s  (%s)\n", n.Host, n.Namespace, n.Model, f.Tag(), format.HumanBytes(f.Size))
	}
	return strings.TrimRight(b.String(), "\n")
}

// checkHuggingFacePipeline rejects repositories whose declared task is not
// text generation before anything is downloaded.
func checkHuggingFacePipeline(n model.Name, pipeline string) error {
	switch strings.ToLower(strings.TrimSpace(pipeline)) {
	case "", "text-generation", "text2text-generation", "conversational", "translation", "summarization", "any-to-any":
		return nil
	default:
		return fmt.Errorf("%s/%s is a %s model; ollamacmf runs text generation models only", n.Namespace, n.Model, pipeline)
	}
}

// fetchHuggingFaceRepo reads a repository's file list from the Hugging Face
// API. A missing repository yields os.ErrNotExist.
func fetchHuggingFaceRepo(ctx context.Context, n model.Name, regOpts *registryOptions) (*huggingFaceRepo, error) {
	requestURL, err := url.Parse(huggingFaceAPI)
	if err != nil {
		return nil, err
	}
	requestURL = requestURL.JoinPath("api", "models", n.Namespace, n.Model)
	requestURL.RawQuery = "blobs=true"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if regOpts != nil && regOpts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+regOpts.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Hugging Face for %s/%s: %w", n.Namespace, n.Model, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: hf.co/%s/%s", os.ErrNotExist, n.Namespace, n.Model)
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("hf.co/%s/%s is private or gated; set HF_TOKEN to a Hugging Face access token", n.Namespace, n.Model)
	default:
		return nil, fmt.Errorf("query Hugging Face for %s/%s: %s", n.Namespace, n.Model, resp.Status)
	}

	var payload struct {
		ID          string `json:"id"`
		PipelineTag string `json:"pipeline_tag"`
		Private     bool   `json:"private"`
		Gated       any    `json:"gated"`
		Siblings    []struct {
			Filename string `json:"rfilename"`
			Size     int64  `json:"size"`
			LFS      *struct {
				SHA256 string `json:"sha256"`
				Size   int64  `json:"size"`
			} `json:"lfs"`
		} `json:"siblings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Hugging Face response for %s/%s: %w", n.Namespace, n.Model, err)
	}

	repo := &huggingFaceRepo{ID: payload.ID, PipelineTag: payload.PipelineTag, Private: payload.Private}
	if gated, ok := payload.Gated.(bool); ok {
		repo.Gated = gated
	} else if payload.Gated != nil {
		repo.Gated = true
	}
	for _, sibling := range payload.Siblings {
		file := huggingFaceFile{Path: sibling.Filename, Size: sibling.Size}
		if sibling.LFS != nil {
			file.SHA256 = sibling.LFS.SHA256
			if file.Size == 0 {
				file.Size = sibling.LFS.Size
			}
		}
		repo.Files = append(repo.Files, file)
	}
	return repo, nil
}
