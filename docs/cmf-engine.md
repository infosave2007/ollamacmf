---
title: CMF maintainer notes
sidebarTitle: Maintainer notes
---

How CMF support is wired into Ollama, what the runtime lock guarantees, and how releases are built. Read this before changing the adapter; users should read the [user guide](cmf.md) instead.

## Components

| Package or file | Role |
| --- | --- |
| `cmfrunner/header.go` | Reads the CMF v2 envelope, header JSON and tensor directory. Produces `Info`: architecture, parameter count, context length, quantization, chat-template capabilities, and the model kind (text, image, audio, decision, skill) |
| `cmfrunner/lock.go`, `cmfrunner/cmf.lock.json` | The pinned Cortiq release, embedded into the binary with `go:embed` |
| `cmfrunner/runtime.go` | Runtime discovery (`FindCortiq`), download and verified install (`Install`), and `Status` for the CLI and API |
| `cmfrunner/client.go` | `llm.LlamaServer` implementation that starts `cortiq serve` on a loopback port and forwards chat and completion requests |
| `server/create.go` | Import path: detects CMF blobs, validates them, fills the manifest config from the header |
| `server/cmf_pull.go` | `ollamacmf pull hf.co/...` for repositories that publish `.cmf` files |
| `server/cmf_routes.go` | `GET /api/cmf/status` and `POST /api/cmf/runtime/install` |
| `server/sched.go` | Creates the CMF client for `model_format: cmf` and keeps CMF residency exclusive |
| `cmd/cmf.go` | The `ollamacmf cmf` command group and the `run FILE.cmf` shortcut |

## Model lifecycle

1. **Import.** A `.cmf` blob reaches the server through `/api/create` (local file or Modelfile) or through the Hugging Face pull. `cmfrunner.ReadInfo` validates the envelope (magic, version 2, no unknown feature bits, section bounds) and parses the header. `Info.Runnable` rejects skills, decision models and non-text pipelines. `applyCMFInfo` writes `model_format: cmf`, family, parameter count, quantization, context length, embedding length, the source model name and the capabilities (`completion`, plus `tools` and `thinking` when the chat template has those branches) into the config layer. The manifest is written with runner `cmf` and format `cmf`.
2. **Load.** The scheduler sees `model_format: cmf` and calls `cmfrunner.NewClient`, which resolves the runtime (installing it if allowed), re-reads the header for the context length and prepares a `cortiq serve <file> --host 127.0.0.1 --port <free port>` command with a restricted environment. CMF residency is exclusive: loading a CMF model unloads other runners and vice versa, because Cortiq does not report memory use to the scheduler.
3. **Serve.** `/api/chat`, formatted `/api/generate`, `/v1/chat/completions` and `/v1/responses` go to Cortiq's `/v1/chat/completions` with streaming on. Raw `/api/generate` and `/v1/completions` go to Cortiq's `/v1/completions`. The adapter maps `temperature`, `top_p`, `seed` and `num_predict` (as `max_tokens`, defaulting to 32 768 capped at the context length), applies `stop` sequences on the Ollama side, splits inline `<think>` blocks into `message.thinking`, and converts tool-call deltas. Options Cortiq cannot take are logged at debug level and dropped.
4. **Unload.** `Close` kills the Cortiq process. Nothing persists between loads.

## What the adapter deliberately does not do

- Render chat templates, tokenize or count tokens. The file owns its template; Cortiq applies it. `num_ctx`, `truncate` and `shift` are therefore not enforced by Ollama.
- Estimate VRAM or choose devices. Cortiq picks CPU, Metal or wgpu itself; `CMF_GPU` is the user's control.
- Serve embeddings, images, structured output or log probabilities. Cortiq's HTTP API has no stable surface for them yet.
- Push CMF models to an Ollama registry. The format has no registry representation; Hugging Face is the distribution channel.

Extending any of these means extending `cmfrunner/client.go` and the request validation in `server/routes.go` together, with tests in `cmfrunner/client_test.go` and `server/cmf_test.go`.

## The runtime lock

`cmfrunner/cmf.lock.json` records one Cortiq release: the tag, the source commit, and for each platform the archive URL, size, SHA-256, format and the name of the executable inside. The same file drives three things:

- the automatic installer in `cmfrunner/runtime.go`;
- the release workflow, which bundles the matching archive next to the binary;
- the CI smoke test, which runs the real runtime against `testdata/cmf/tiny-v0.8.19.cmf`.

Updating the lock is described in [runtime updates](cmf-release-automation.md). Never point the lock at `latest` or at a build for another architecture: the installer compares the Go build target with the lock entry and refuses mismatches.

## Releases

`.github/workflows/release-ollamacmf.yml` runs on every `v*` tag. It builds `ollamacmf` with `go build` on native runners for macOS (arm64 and amd64), Linux (amd64 and arm64) and Windows amd64, downloads and verifies the Cortiq archive for each target from the lock, and publishes `ollamacmf-<os>-<arch>.tar.gz` or `.zip` plus `checksums.txt` on a GitHub release. `install.sh` and `install.ps1` download from exactly those assets.

Upstream Ollama's `release.yaml` and `latest.yaml` are kept for reference but only run on manual dispatch: they need Ollama Inc.'s signing secrets and self-hosted runners.

To cut a release:

```sh
git tag v0.2.0
git push origin v0.2.0
```

## Tests

| Test | Covers |
| --- | --- |
| `go test ./cmfrunner` | Lock parsing, runtime discovery order, verified install from a fake release, header parsing on the fixture, stop sequences, option mapping, SSE and JSON decoding |
| `go test ./server -run 'CMF\|HuggingFace'` | Import validation, config from header, pull from a fake Hugging Face, request rejections, scheduler residency |
| `CMF_E2E=1 OLLAMA_CORTIQ_PATH=... CMF_E2E_MODEL=... go test ./cmfrunner -run TestCortiqDownloadedRuntimeE2E` | The real runtime against the tiny fixture; run by `cmf-runner-smoke.yml` in CI |

The checked-in fixture `testdata/cmf/tiny-v0.8.19.cmf` is a real, deterministic 51 KB model with random weights. It validates the integration, not model quality.
