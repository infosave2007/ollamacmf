---
title: CMF user guide
sidebarTitle: User guide
---

This guide covers everything about running CMF models with `ollamacmf`: installing, getting models, configuring the server, using the API and fixing problems. For a two-minute overview start with the [README](https://github.com/infosave2007/ollamacmf#readme).

## Install

Pick one:

- **Installer script.** `curl -fsSL https://raw.githubusercontent.com/infosave2007/ollamacmf/main/install.sh | sh` on macOS and Linux, `irm https://raw.githubusercontent.com/infosave2007/ollamacmf/main/install.ps1 | iex` on Windows. The script downloads the latest release, verifies its checksum and installs `ollamacmf` plus the Cortiq runtime into `/usr/local/bin` (falling back to `~/.local/bin`) or `%LOCALAPPDATA%\Programs\ollamacmf`.
- **Release archive.** Download `ollamacmf-<os>-<arch>.tar.gz` or `.zip` from the [releases page](https://github.com/infosave2007/ollamacmf/releases/latest). The archive is self-contained.
- **Source.** `go build -o ollamacmf .` in a checkout. This needs Go 1.26 and a C compiler; the resulting binary serves CMF models and, when the native runners are built as described in [development.md](development.md), GGUF and safetensors models as well.

Upgrading is the same as installing: run the installer again or replace the binary. Models and the runtime are kept.

## Start the server

```sh
ollamacmf serve
```

The server listens on `127.0.0.1:11434` by default and keeps running until you stop it with Ctrl-C. On macOS and Windows you can also run it from a login item or a service manager; it needs no other setup.

To run it alongside an unmodified Ollama, give it a different port:

```sh
OLLAMA_HOST=127.0.0.1:11435 ollamacmf serve
```

and set the same `OLLAMA_HOST` in every terminal that uses the `ollamacmf` client.

## Get a model

### From Hugging Face

```sh
ollamacmf pull hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp
```

`hf.co/<account>/<repository>` names a repository; the tag after the colon names one of its `.cmf` files. Rules:

- A repository with a single CMF file needs no tag.
- The tag is the file name without `.cmf`. A fragment is enough when it matches exactly one file (`:8b-q4tp`).
- Without a usable tag the command fails with the list of files, their sizes and the exact command for each.
- Files that accompany a model (`*.mtp.cmf` draft heads, `*.mm.cmf` projectors, `*.skill.cmf` overlays) are never chosen by default.
- Repositories whose task is not text generation (speech, image, video, embeddings, decision) are refused before anything is downloaded.

Downloads go into the Ollama blob store, resume after an interruption and are verified against the SHA-256 Hugging Face publishes for the file. Set `HF_TOKEN` to a Hugging Face access token for private or gated repositories.

`ollamacmf run hf.co/...` pulls the model first when it is not installed yet, exactly like `ollama run`.

### From a local file

```sh
ollamacmf run ./model.cmf                       # import under the file's name, then chat
ollamacmf cmf import ./model.cmf --name mymodel # import under a chosen name
ollamacmf cmf import ./model.cmf --system "You answer in French."
```

The file is validated (CMF v2 container, text generation model, feature bits the runtime understands) and copied into the model store. The original can be deleted afterwards.

### Browse what is available

```sh
ollamacmf cmf search          # all CMF repositories of the default account
ollamacmf cmf search qwen     # filter by name
ollamacmf cmf search --author someone-else
```

The [model catalog](cmf-models.md) lists the published models with sizes.

## Work with models

| Command | What it does |
| --- | --- |
| `ollamacmf list` | Installed models with size and age |
| `ollamacmf show NAME` | Architecture, parameter count, context length, quantization, capabilities, Modelfile |
| `ollamacmf ps` | Loaded models and when they will be unloaded |
| `ollamacmf stop NAME` | Unload a model now |
| `ollamacmf cp NAME NEW` | Copy under another name |
| `ollamacmf rm NAME` | Delete a model and its file |
| `ollamacmf cmf status` | Runtime, server and CMF models at a glance |

Models stay loaded for five minutes after the last request (`OLLAMA_KEEP_ALIVE`). One Cortiq process serves one loaded model; loading a different model unloads the previous one.

### Modelfiles

A derived model keeps the base weights and adds a system prompt, example messages or parameters:

```text
FROM hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp
SYSTEM You are a helpful assistant. Keep answers short.
PARAMETER temperature 0.4
PARAMETER num_predict 1024
PARAMETER stop "###"
```

```sh
ollamacmf create short-granite -f Modelfile
```

`FROM` accepts a model name or an absolute path to a `.cmf` file. `TEMPLATE`, `RENDERER` and `PARSER` are rejected for CMF models because the chat template is part of the file. `ADAPTER`, `DRAFT` and create-time quantization are not supported either.

## Chat and generate

### Interactive

```sh
ollamacmf run hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp
```

Inside the session `/set system`, `/set parameter`, `/show info` and the other Ollama commands work. `--think=false` disables the reasoning mode of models that have one; `--verbose` prints token counts and timings.

### Ollama API

`POST /api/chat` and `POST /api/generate` behave as documented for Ollama. Streaming is on by default.

```sh
curl http://localhost:11434/api/chat -d '{
  "model": "hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp",
  "messages": [{"role": "user", "content": "Write a haiku about Go."}],
  "stream": false,
  "options": {"temperature": 0.7, "num_predict": 128}
}'
```

Tool calls are passed to the model when its chat template supports them and returned in `message.tool_calls`. Reasoning text from models with a thinking mode arrives in `message.thinking`.

### OpenAI-compatible API

`/v1/chat/completions`, `/v1/completions` and `/v1/models` accept OpenAI clients. Use any string as the API key.

```sh
curl http://localhost:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp", "messages": [{"role": "user", "content": "Hello"}]}'
```

### Request options

| Option | Effect |
| --- | --- |
| `temperature`, `top_p`, `seed` | Forwarded to Cortiq |
| `num_predict` | Maximum tokens in the answer. Unset or `-1` allows up to 32 768 tokens, capped at the model's context length |
| `stop` | The answer is cut at the first stop sequence |
| `top_k`, `min_p`, `repeat_penalty`, `presence_penalty`, `frequency_penalty`, `num_ctx`, `num_gpu`, `num_thread`, `num_batch` | Accepted and ignored; Cortiq has no equivalent, and the context length comes from the model |
| `format`, `logprobs` | Rejected with HTTP 400 |

Raw completion (`/api/generate` with `"raw": true` or `/v1/completions`) forwards `temperature` and `num_predict` only.

## Configuration

| Variable | Purpose | Default |
| --- | --- | --- |
| `OLLAMA_HOST` | Listen address of the server, and the address the client talks to | `127.0.0.1:11434` |
| `OLLAMA_MODELS` | Model store | `~/.ollama/models` |
| `OLLAMA_KEEP_ALIVE` | Idle time before a model is unloaded | `5m` |
| `OLLAMA_CORTIQ_DIR` | Managed runtime directory | `<models dir>/../cortiq`, usually `~/.ollama/cortiq` |
| `OLLAMA_CORTIQ_PATH` | Explicit Cortiq executable; nothing else is consulted when set | unset |
| `OLLAMA_CORTIQ_AUTO_INSTALL` | `0` disables downloading a missing runtime | `1` |
| `CMF_GPU` | `1` requests the GPU, `0` forces the CPU | Cortiq decides |
| `CMF_THREADS` | CPU threads for inference | all cores |
| `CMF_SERVE_SLOTS` | Concurrent sequences per Cortiq process | `1` |
| `HF_TOKEN` | Hugging Face token for private repositories | unset |
| `OLLAMA_DEBUG` | `1` for verbose server logs | unset |

The Cortiq process receives only a small, explicit environment: locale and temp-dir basics, loader paths, and every variable whose name starts with `CMF_`, `CUDA_`, `HIP_`, `HSA_`, `ROCR_`, `ROCM_`, `NVIDIA_`, `VK_`, `METAL_`, `OMP_`, `MKL_`, `RAYON_` or `RUST_`. Other variables of the server process, including API credentials, are not passed on.

### Runtime management

```sh
ollamacmf cmf status   # which runtime, where from, which version is expected
ollamacmf cmf setup    # download and install the pinned runtime now
```

The pinned release and the checksums of its builds live in [`cmfrunner/cmf.lock.json`](https://github.com/infosave2007/ollamacmf/blob/main/cmfrunner/cmf.lock.json) and are compiled into the binary. The server resolves the runtime in this order and uses the first match:

1. `OLLAMA_CORTIQ_PATH`
2. The managed install `OLLAMA_CORTIQ_DIR/<version>/cortiq`
3. A `cortiq` binary next to the `ollamacmf` executable (release archives ship one) or in `lib/ollama`
4. `cortiq` on `PATH`
5. Otherwise it downloads the pinned release for the current platform, verifies the archive's size and SHA-256 and installs it as the managed runtime

A runtime found through `OLLAMA_CORTIQ_PATH` or `PATH` is used even when its version differs from the pinned one; the server log notes the difference. `cmf setup` installs the managed, pinned runtime through the running server, or locally when no server is running.

Published Cortiq builds exist for macOS (arm64, x86-64), Linux x86-64 and Windows (x86-64, ARM64). On Linux ARM64 build Cortiq with `cargo install cortiq-cli` and set `OLLAMA_CORTIQ_PATH`.

## Troubleshooting

| Symptom | What to do |
| --- | --- |
| `CMF runtime is not available` | Run `ollamacmf cmf status`; then `ollamacmf cmf setup`, or set `OLLAMA_CORTIQ_PATH`. Offline machines: copy the archive named in the lock into the directory `cmf status` prints and unpack it there |
| `CMF runtime is not available for this platform` | No published Cortiq build. Build Cortiq from source and set `OLLAMA_CORTIQ_PATH` |
| `the repository has several CMF files` | Add a tag; the message lists the options |
| `no CMF file matches the requested tag` | Check the spelling against the list in the message |
| `this is a CMF ... model; ollamacmf runs text generation models only` | The file is for another Cortiq pipeline (image, audio, video, embeddings, decision) |
| `invalid CMF file` | The file is not a CMF v2 container, is truncated, or needs a newer Cortiq than the pinned one. Run `cortiq verify` on it |
| `CMF runner failed` or `timeout waiting for CMF runner` | Cortiq could not load the model. The server log contains Cortiq's own message, usually a memory or platform problem |
| Answers end mid-sentence | Raise `num_predict` |
| Open WebUI or another client gets HTTP 400 | Check the error text: `format` and `logprobs` are not supported for CMF models |
| `ollamacmf ps` reports 100% CPU | Cortiq does not report device placement; the column is not meaningful for CMF. The server log shows the backend Cortiq selected |

Start the server with `OLLAMA_DEBUG=1` to see every option the adapter ignores and the full Cortiq output.

## Security notes

The Cortiq process listens on an ephemeral `127.0.0.1` port without authentication, like Ollama's own runner processes. Any process on the same machine could connect to it, so treat `ollamacmf` the way you treat Ollama: fine on a personal machine or a trusted server, not something to expose to untrusted local users. Do not forward the Cortiq port; clients should always talk to the `ollamacmf` server.

Downloads are verified before use: Cortiq archives against the SHA-256 recorded in the lock, model files against the SHA-256 Hugging Face publishes. Models from any source are still untrusted data; only use models you trust.
