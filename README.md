<div align="center">

# ollamacmf

**Ollama with native support for CMF models.**

[![Release](https://img.shields.io/github/v/release/infosave2007/ollamacmf?label=release)](https://github.com/infosave2007/ollamacmf/releases/latest)
[![Build](https://github.com/infosave2007/ollamacmf/actions/workflows/release-ollamacmf.yml/badge.svg)](https://github.com/infosave2007/ollamacmf/actions/workflows/release-ollamacmf.yml)
[![CMF models](https://img.shields.io/badge/CMF%20models-Hugging%20Face-ffd21e?logo=huggingface)](https://huggingface.co/infosave)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[Install](#install) · [Quick start](#quick-start) · [Models](#models) · [Use from apps](#use-it-from-your-apps) · [Configuration](#configuration) · [Troubleshooting](#troubleshooting) · [Русский](README.ru.md)

</div>

`ollamacmf` is a fork of [Ollama](https://github.com/ollama/ollama) that runs [CMF](https://github.com/infosave2007/cmf) models, the compact single-file format produced by Cortiq. Everything you already know from Ollama works the same way: `pull`, `run`, `list`, `rm`, the REST API, the OpenAI-compatible API, and every app that talks to Ollama.

```sh
ollamacmf run hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp
```

The Cortiq runtime that executes CMF models is downloaded, verified and installed automatically on first use. There is nothing to configure.

## Install

**macOS and Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/infosave2007/ollamacmf/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/infosave2007/ollamacmf/main/install.ps1 | iex
```

**Manual download.** Grab the archive for your platform from the [latest release](https://github.com/infosave2007/ollamacmf/releases/latest), unpack it anywhere and run `ollamacmf` from there. Each archive contains the `ollamacmf` binary and the matching Cortiq runtime.

**From source** (Go 1.26 or newer, a C compiler)

```sh
git clone https://github.com/infosave2007/ollamacmf.git
cd ollamacmf
go build -o ollamacmf .
```

`ollamacmf` can run next to a regular Ollama installation. It uses the same default port (`11434`), so stop one before starting the other, or give `ollamacmf` its own port with `OLLAMA_HOST=127.0.0.1:11435`.

## Quick start

Start the server in one terminal:

```sh
ollamacmf serve
```

Chat with a model in another:

```sh
ollamacmf run hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp
```

The first run downloads the Cortiq runtime (about 16 MB) and the model (126 MB for this one), then opens an interactive chat. Type `/bye` to leave.

Check that everything is in place at any time:

```sh
ollamacmf cmf status
```

```text
Server:    running at http://127.0.0.1:11434 (ollamacmf 0.1.0)
Platform:  darwin/arm64
Runtime:   Cortiq installed (0.8.19, managed)
           /Users/you/.ollama/cortiq/v0.8.19/cortiq
Expected:  Cortiq v0.8.19 in /Users/you/.ollama/cortiq

CMF models:
NAME                                          ARCH    PARAMS     QUANT       SIZE
hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp    lfm2    229.69M    q4_block    132 MB
```

## Models

### Find models

CMF models are published on Hugging Face under [huggingface.co/infosave](https://huggingface.co/infosave). List them from the command line:

```sh
ollamacmf cmf search            # everything
ollamacmf cmf search granite    # filter by name
```

See [docs/cmf-models.md](docs/cmf-models.md) for a curated list with sizes.

### Pull from Hugging Face

A repository is addressed as `hf.co/<account>/<repository>`. When it holds a single CMF file that is all you need:

```sh
ollamacmf pull hf.co/infosave/Bonsai-1.7Bcmf
```

When it holds several variants, pick one with a tag. The tag is the file name without `.cmf`; a unique fragment of it works too:

```sh
ollamacmf pull hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp
ollamacmf pull hf.co/infosave/Granite-4.2-cmf:8b-q4tp
```

If the tag is missing or ambiguous, `ollamacmf` lists the available files with their sizes and the exact command for each. Downloads resume if interrupted, and every file is checked against its SHA-256 before it is registered. Private or gated repositories work with `HF_TOKEN` set to a Hugging Face access token.

### Use a local file

Already have a `.cmf` file, for example one you converted with `cortiq convert`? Run it directly:

```sh
ollamacmf run ./granite-4.2-3b-q4tp.cmf
```

or register it under a name of your choice:

```sh
ollamacmf cmf import ./granite-4.2-3b-q4tp.cmf --name granite
ollamacmf run granite
```

Only text generation models are accepted. Image, video, audio, embedding and decision CMF files are refused with a message that says what they are.

### Manage models

The usual Ollama commands apply:

```sh
ollamacmf list                 # installed models
ollamacmf show granite         # architecture, parameters, context length, quantization, capabilities
ollamacmf ps                   # loaded models
ollamacmf stop granite         # unload now
ollamacmf cp granite my-granite
ollamacmf rm granite
```

### Customize with a Modelfile

Derive a model with a system prompt or different defaults exactly as with any Ollama model:

```text
FROM hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp
SYSTEM You are a concise assistant. Answer in one paragraph.
PARAMETER temperature 0.3
PARAMETER num_predict 512
```

```sh
ollamacmf create concise-granite -f Modelfile
```

`FROM` can also point straight at a `.cmf` file. `TEMPLATE`, `RENDERER` and `PARSER` are not used: a CMF file carries its own chat template and tokenizer.

## Use it from your apps

`ollamacmf` serves the standard Ollama API and the OpenAI-compatible API, so existing clients work unchanged. Point them at `http://localhost:11434`.

**Ollama API**

```sh
curl http://localhost:11434/api/chat -d '{
  "model": "hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp",
  "messages": [{"role": "user", "content": "Why is the sky blue?"}]
}'
```

**OpenAI SDK**

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:11434/v1", api_key="ollama")
reply = client.chat.completions.create(
    model="hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp",
    messages=[{"role": "user", "content": "Why is the sky blue?"}],
)
print(reply.choices[0].message.content)
```

**Open WebUI, Continue, Zed, Cline and others** connect the same way they connect to Ollama. Use `ollamacmf list` to see the model names to enter.

### What works

| Feature | Status |
| --- | --- |
| Chat and generate, streaming | Yes |
| Tool calling | Yes, when the model's chat template supports tools (`ollamacmf show` lists `tools`) |
| Thinking / reasoning | Yes, when the model supports it (`ollamacmf show` lists `thinking`); `--think=false` turns it off |
| `temperature`, `top_p`, `seed`, `num_predict`, `stop` | Yes |
| `top_k`, `min_p`, repetition penalties, `num_ctx`, `num_gpu` | Accepted and ignored: Cortiq controls sampling and context itself |
| Structured output (`format`), log probabilities | Not yet |
| Images and audio input, embeddings | Not yet |
| `ollamacmf push` to ollama.com | No. Share the `.cmf` file or publish it on Hugging Face |

GGUF and safetensors models keep working exactly as in upstream Ollama when the corresponding native runners are built; a plain `go build` gives you a binary that serves CMF models only.

## Configuration

Everything has a sensible default. The variables below are read by `ollamacmf serve`.

| Variable | Purpose | Default |
| --- | --- | --- |
| `OLLAMA_HOST` | Address the server listens on | `127.0.0.1:11434` |
| `OLLAMA_MODELS` | Where models are stored | `~/.ollama/models` |
| `OLLAMA_KEEP_ALIVE` | How long a model stays loaded after the last request | `5m` |
| `OLLAMA_CORTIQ_DIR` | Where the Cortiq runtime is installed | `cortiq` next to the models directory |
| `OLLAMA_CORTIQ_PATH` | Use this Cortiq executable instead of the managed one | unset |
| `OLLAMA_CORTIQ_AUTO_INSTALL` | Download the runtime automatically when missing (`0` to disable) | `1` |
| `CMF_GPU` | `1` asks Cortiq for the GPU (Metal on Apple Silicon, Vulkan/DX12 elsewhere), `0` forces CPU | Cortiq's default |
| `CMF_THREADS` | CPU threads for Cortiq | all cores |
| `HF_TOKEN` | Hugging Face token for private or gated repositories | unset |

Variables starting with `CMF_` are passed through to Cortiq; see the [Cortiq documentation](https://github.com/infosave2007/cmf) for the full list.

### The Cortiq runtime

`ollamacmf` is tested against one exact Cortiq release, recorded in [`cmfrunner/cmf.lock.json`](cmfrunner/cmf.lock.json) together with the SHA-256 of every build. On first use the server downloads that release for your platform, verifies the checksum and installs it under `~/.ollama/cortiq/<version>/`. Release archives ship the same runtime next to the binary, so an offline install needs no download.

```sh
ollamacmf cmf setup     # install the runtime now instead of on first use
ollamacmf cmf status    # show which runtime is used and where it came from
```

Lookup order: `OLLAMA_CORTIQ_PATH`, the managed install, a `cortiq` binary next to `ollamacmf`, then `cortiq` on `PATH`. A runtime from `PATH` with a different version is used but reported in the server log.

Supported platforms: macOS (Apple Silicon and Intel), Linux x86-64, Windows x86-64 and ARM64. Linux ARM64 has no published Cortiq build yet; build Cortiq from source there and set `OLLAMA_CORTIQ_PATH`.

## Troubleshooting

**`CMF runtime is not available`** — the server could not find or download Cortiq. Run `ollamacmf cmf status` to see why, then `ollamacmf cmf setup` to install it, or set `OLLAMA_CORTIQ_PATH` to a Cortiq executable. If the machine is offline, copy the archive named in `cmfrunner/cmf.lock.json` over and unpack it into the directory `cmf status` prints.

**`the repository has several CMF files`** — add a tag to choose a file; the message lists them.

**`this is a CMF image model`** (or audio, video, embedding, decision) — only text generation models run here. Use Cortiq directly for the others.

**The answer stops early** — raise `num_predict` (in a Modelfile or the request options). Without it `ollamacmf` allows up to 32 768 tokens or the model's context, whichever is smaller.

**Another Ollama is already running** — both use port 11434. Stop one, or start `ollamacmf` with `OLLAMA_HOST=127.0.0.1:11435 ollamacmf serve` and set the same `OLLAMA_HOST` in the terminal you use the client from.

**`ollamacmf ps` shows `100% CPU`** — Cortiq decides device placement itself and does not report it back, so this column is not meaningful for CMF models. Check the server log for the backend Cortiq picked.

Server logs go to the terminal that runs `ollamacmf serve`; add `OLLAMA_DEBUG=1` for more detail.

## How it works

```text
ollamacmf CLI / Ollama API / OpenAI API
                 │
                 ▼
          ollamacmf server          model store, scheduling, HTTP API
                 │
                 ▼
     cmfrunner → cortiq serve       one Cortiq process per loaded model,
                 │                  loopback only, started and stopped by the server
                 ▼
            model.cmf               weights, tokenizer and chat template in one file
```

Cortiq owns tokenization, the chat template, sampling and the context window; `ollamacmf` forwards chat and completion requests to it and translates the answers into the Ollama and OpenAI response formats. The Cortiq port is picked per process on `127.0.0.1` and is not meant to be used directly.

## Documentation

- [User guide](docs/cmf.md) — install, pull, import, configure, API, troubleshooting in more depth
- [Model catalog](docs/cmf-models.md) — CMF models on Hugging Face with sizes and pull commands
- [Maintainer notes](docs/cmf-engine.md) — how the adapter, the runtime lock and the release pipeline fit together
- [Runtime updates](docs/cmf-release-automation.md) — how a new Cortiq release gets into the lock
- [Development](docs/development.md) — building the native Ollama runners

## Contributing and security

Bug reports and ideas go to the [issue tracker](https://github.com/infosave2007/ollamacmf/issues); see [CONTRIBUTING.md](CONTRIBUTING.md). Please report security issues privately through [GitHub Security Advisories](https://github.com/infosave2007/ollamacmf/security/advisories/new) as described in [SECURITY.md](SECURITY.md).

`ollamacmf` is an independent project and is not affiliated with Ollama, Inc. It is distributed under the [MIT License](LICENSE) inherited from Ollama. The bundled Cortiq runtime is licensed under Apache-2.0.
