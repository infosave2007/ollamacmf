---
title: CMF/Cortiq guide
sidebarTitle: CMF/Cortiq
---

`ollamacmf` is an independent, experimental fork of Ollama. It imports one
local, runnable CMF v2 generative text model and starts
[Cortiq](https://github.com/infosave2007/cmf) as a sidecar subprocess. Ollama
continues to own the local model store, scheduling, and public API.

This page is the user and operator guide. For the supply-chain and runner
contract behind it, see the [CMF engine contract](cmf-engine.md). For the
candidate-only upstream runtime workflow, see [CMF release automation](cmf-release-automation.md).

> [!WARNING]
> This is not a production deployment mode. The Cortiq sidecar uses
> unauthenticated HTTP on loopback and the present bootstrap has a local port
> bind race. Use it only on a trusted, single-user host. Do not expose the
> sidecar port, use it as a security boundary, or send sensitive prompts/model
> data through it on a host where another local process is untrusted.

## What is and is not distributed

| Item | Status |
| --- | --- |
| `ollamacmf` source code | Available in this repository |
| CMF-enabled release binary or installer | Not published |
| CMF-ready Docker image | Not published |
| Cortiq executable | Not bundled; select and verify an asset from `cmf.lock.json` |
| CMF registry transport | Not implemented; CMF pull and push are rejected |
| Automatic CMF release publishing | Not implemented; automation proposes a reviewable lock candidate only |

Do **not** run an official Ollama installer, use the upstream Docker image, or
call `ollama pull` expecting it to add CMF support. Build this repository and
configure a verified Cortiq executable instead.

## Platform and runtime scope

[`cmf.lock.json`](https://github.com/infosave2007/ollamacmf/blob/main/cmf.lock.json) is the source of truth for the runtime
inputs reviewed by a revision of this fork. Its current records are intentionally
narrow:

| Host | Locked Cortiq asset | Evidence in this fork | Status |
| --- | --- | --- | --- |
| Linux `amd64` | `cortiq-x86_64-unknown-linux-gnu.tar.gz` | Archive validation and adapter-level E2E in GitHub Actions | Experimental source flow |
| macOS `arm64` | `cortiq-aarch64-apple-darwin.tar.gz` | Archive validation | Reviewed input only |
| macOS `amd64` | `cortiq-x86_64-apple-darwin.tar.gz` | Archive validation and local adapter smoke under Rosetta | Reviewed input only; not native-Intel coverage |
| Windows `amd64` | `cortiq-x86_64-pc-windows-msvc.zip` | Archive validation and safe extraction under PowerShell | Reviewed input only; no Windows adapter E2E |
| Windows `arm64` | `cortiq-aarch64-pc-windows-msvc.zip` | Archive validation | Reviewed input only |
| Linux `arm64` | None | The pinned release has no reviewed `aarch64-unknown-linux-gnu` archive | Unsupported |

“Reviewed input” is not a claim of a packaged or production-supported runtime.
Never substitute an artifact for a different architecture. An architecture
mismatch may let import complete but fail when the server starts the sidecar.

## Before you start

You need all of the following:

1. A checkout of this repository and a build of **this fork**.
2. A Cortiq runtime whose platform, archive checksum, and executable entry match
   the checkout's `cmf.lock.json`.
3. A trusted, runnable generative CMF v2 text model. CMF skill and decision
   files are not generation models and are rejected at import.
4. A trusted single-user host. Read [Security boundary](#security-boundary).

The checked-in [`testdata/cmf/tiny-v0.8.19.cmf`](https://github.com/infosave2007/ollamacmf/blob/main/testdata/cmf/tiny-v0.8.19.cmf)
is a real, deterministic contract fixture, not a useful language model. It is
appropriate for the smoke test below.

## Obtaining a CMF model

This fork does not convert, download, or distribute model weights. Start only
with a model you are authorized to use and have independently reviewed. At the
same upstream CMF commit recorded in this fork's lock, the
[Cortiq quick start](https://github.com/infosave2007/cmf/blob/5d6f1830cfec6ed1cdd1eca9e9c259a66c998716/README.md#quick-start)
documents `cortiq convert` for supported source models and
`cortiq import-gguf` for an existing GGUF file, followed by `cortiq verify`.
The upstream [model-card and conversion notes](https://github.com/infosave2007/cmf/blob/5d6f1830cfec6ed1cdd1eca9e9c259a66c998716/docs/hf/README.md)
are a useful starting point for model-specific constraints.

Those external tools and artifacts are not bundled, endorsed, or automatically
validated by `ollamacmf`. Review the source model's license, provenance,
conversion output, and Cortiq version separately. A valid CMF file can still
be outside this adapter's scope (for example, a skill, decision, media, or
otherwise non-runnable text-generation file). Import it locally and run the
isolated API smoke test before relying on it.

## macOS/Linux quick start

The repository root [README](https://github.com/infosave2007/ollamacmf/blob/main/README.md#verified-local-smoke-test-macoslinux)
contains one copyable end-to-end smoke test. It has been exercised with a
build-target-matched Cortiq archive, an isolated server port/model store, and the tiny
fixture. The condensed sequence is:

1. `go build -o ./ollamacmf .` in this checkout.
2. Pick the exact archive from `cmf.lock.json` for `go env GOOS` and
   `go env GOARCH`; compare the downloaded archive and checksum sidecar
   directly with their locked sizes and SHA-256 values, then use the tracked
   safe extractor before setting `OLLAMA_CORTIQ_PATH` to the resulting
   **absolute** executable path.
3. Start `ollamacmf serve` with isolated `OLLAMA_HOST` and `OLLAMA_MODELS`
   values.
4. Import an absolute `.cmf` path through a Modelfile, then call the ordinary
   Ollama endpoint.

The full README commands intentionally never use `latest`, `curl | sh`, or an
unverified `PATH` executable.

### Import a real model

After the server is running, make a Modelfile using one absolute CMF path:

```text
FROM /absolute/path/to/model.cmf
PARAMETER temperature 0.7
PARAMETER num_predict 128
```

Then create and run it with the fork binary connected to the same `OLLAMA_HOST`:

```sh
"$OLLAMA_BIN" create local-cmf -f /absolute/path/to/Modelfile.cmf
"$OLLAMA_BIN" run local-cmf
```

A CMF import accepts exactly one model file. The fork validates its fixed CMF
envelope (version, required feature bits, runnable model kind, and section
bounds) before creating a manifest. Cortiq validates the opaque model internals
when it loads the model.

The import intentionally rejects multiple/split CMF files, drafts,
create-time quantization, `TEMPLATE`, `RENDERER`, `PARSER`, image messages, and
model parameters other than `temperature` and `num_predict`. A model derived
from another CMF model is checked against the same restrictions.

### Make requests

Call the regular Ollama server, never the Cortiq sidecar port:

```sh
curl --fail --silent "http://${OLLAMA_HOST}/api/chat" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "local-cmf",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": false,
    "options": {"temperature": 0.7, "num_predict": 128}
  }'
```

`ollama run`, `/api/chat`, and a formatted `/api/generate` become Cortiq chat
requests, so Cortiq handles tokenization and prompt formatting. A raw
`/api/generate` request (`"raw": true`) becomes a Cortiq completion request.
The raw endpoint is non-streaming in this adapter.

## Windows PowerShell runtime setup

Windows archives are locked inputs but do not yet have adapter E2E coverage in
this fork. The following procedure requires a `windows/amd64` or
`windows/arm64` Go build target, selects its exact lock entry, checks both the
archive and checksum sidecar directly against their locked sizes and SHA-256
values, and uses the tracked Python verifier to extract without executing
Cortiq. Run it from the repository root after
`go build -o .\ollamacmf.exe .`; it requires Python 3 available as `py`,
`python3`, or `python`:

```powershell
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Fail([string]$Message) {
  throw "error: $Message"
}

function Assert-Sha256([object]$Value, [string]$Name) {
  $text = [string]$Value
  if ($text -cnotmatch '^[0-9a-f]{64}$') {
    Fail "$Name is not a lowercase 64-character SHA-256"
  }
  return $text
}

function Assert-PositiveDecimal([object]$Value, [string]$Name) {
  $text = [string]$Value
  if ($text -cnotmatch '^[1-9][0-9]*$') {
    Fail "$Name is not a positive decimal byte count"
  }
  try {
    [Int64]$number = $text
  } catch {
    Fail "$Name is outside the supported byte-count range"
  }
  return $number
}

function Assert-SafeFileName([object]$Value, [string]$Name) {
  $text = [string]$Value
  if ($text -ceq '.' -or $text -ceq '..' -or $text -cnotmatch '^[A-Za-z0-9._-]+$') {
    Fail "$Name is not a safe file name"
  }
  return $text
}

function Assert-HttpsUri([object]$Value, [string]$Name) {
  $text = [string]$Value
  try {
    $uri = [System.Uri]$text
  } catch {
    Fail "$Name is not an absolute HTTPS URL"
  }
  if (-not $uri.IsAbsoluteUri -or $uri.Scheme -cne 'https') {
    Fail "$Name is not an absolute HTTPS URL"
  }
  return $text
}

function Get-Sha256([string]$Path) {
  return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

$root = (Get-Location).Path
$lockPath = Join-Path $root 'cmf.lock.json'
$verifier = Join-Path $root '.github/scripts/verify_cmf_runtime_archive.py'
if (-not (Test-Path -LiteralPath $lockPath -PathType Leaf)) { Fail 'run this from the checkout root' }
if (-not (Test-Path -LiteralPath $verifier -PathType Leaf)) { Fail 'CMF archive verifier is missing' }

$goOS = (& go env GOOS).Trim()
$goArch = (& go env GOARCH).Trim()
if ($LASTEXITCODE -ne 0 -or $goOS -cne 'windows' -or $goArch -notin @('amd64', 'arm64')) {
  Fail "Windows guide requires a windows/amd64 or windows/arm64 Go build target; got $goOS/$goArch"
}
$target = "windows/$goArch"
$lock = Get-Content -LiteralPath $lockPath -Raw | ConvertFrom-Json
$matches = @($lock.artifacts | Where-Object { $_.platform -ceq $target })
if ($matches.Count -ne 1) { Fail "no unique reviewed runtime for $target" }
$asset = $matches[0]

$archiveUrl = Assert-HttpsUri $asset.archive.url 'archive URL'
$archiveName = Assert-SafeFileName $asset.archive.name 'archive name'
$archiveSize = Assert-PositiveDecimal $asset.archive.sizeBytes 'archive size'
$archiveSha256 = Assert-Sha256 $asset.archive.sha256 'locked archive SHA-256'
$archiveFormat = [string]$asset.archive.format
if ($archiveFormat -cne 'zip') { Fail "unsupported Windows archive format: $archiveFormat" }
$entries = @($asset.archive.entries)
if ($entries.Count -ne 1) { Fail 'lock must contain exactly one archive entry' }
$archiveEntry = Assert-SafeFileName $entries[0] 'archive entry'

$checksumUrl = Assert-HttpsUri $asset.checksum.url 'checksum URL'
$checksumName = Assert-SafeFileName $asset.checksum.name 'checksum name'
$checksumSize = Assert-PositiveDecimal $asset.checksum.sizeBytes 'checksum size'
$checksumSha256 = Assert-Sha256 $asset.checksum.sha256 'locked checksum SHA-256'
$executable = Assert-SafeFileName $asset.installedExecutable 'installed executable'
if ($archiveEntry -cne $executable) { Fail 'lock archive entry and installed executable disagree' }

$runtime = Join-Path $root '.runtime\cortiq'
New-Item -ItemType Directory -Force -Path $runtime | Out-Null
if (-not (Test-Path -LiteralPath $runtime -PathType Container)) { Fail 'runtime path is not a directory' }
$archive = Join-Path $runtime $archiveName
$checksum = Join-Path $runtime $checksumName
if (Test-Path -LiteralPath (Join-Path $runtime $executable)) { Fail "refusing to overwrite existing $executable" }

Invoke-WebRequest -UseBasicParsing -Uri $archiveUrl -OutFile $archive
if ((Get-Item -LiteralPath $archive).Length -ne $archiveSize) { Fail 'downloaded archive size differs from lock' }
if ((Get-Sha256 $archive) -cne $archiveSha256) { Fail 'downloaded archive SHA-256 differs from lock' }

Invoke-WebRequest -UseBasicParsing -Uri $checksumUrl -OutFile $checksum
if ((Get-Item -LiteralPath $checksum).Length -ne $checksumSize) { Fail 'downloaded checksum size differs from lock' }
if ((Get-Sha256 $checksum) -cne $checksumSha256) { Fail 'downloaded checksum asset SHA-256 differs from lock' }

$checksumLines = @(Get-Content -LiteralPath $checksum)
if ($checksumLines.Count -ne 1) { Fail 'checksum asset must contain exactly one line' }
if ($checksumLines[0].TrimEnd("`r") -cnotmatch '^(?<hash>[0-9A-Fa-f]{64})\s+\*?(?<name>[A-Za-z0-9._-]+)$') {
  Fail 'checksum asset must contain a digest and file name'
}
$sidecarArchiveSha256 = $Matches.hash.ToLowerInvariant()
$sidecarArchiveName = $Matches.name
if ($sidecarArchiveName -cne $archiveName) { Fail 'release checksum names a different archive' }
if ($sidecarArchiveSha256 -cne $archiveSha256) { Fail 'release checksum disagrees with the lock' }

$pythonCommand = $null
$pythonPrefix = @()
if (Get-Command py -ErrorAction SilentlyContinue) {
  $pythonCommand = 'py'
  $pythonPrefix = @('-3')
} elseif (Get-Command python3 -ErrorAction SilentlyContinue) {
  $pythonCommand = 'python3'
} elseif (Get-Command python -ErrorAction SilentlyContinue) {
  $pythonCommand = 'python'
} else {
  Fail 'Python 3 is required for safe archive extraction'
}
$pythonVersionArgs = @() + $pythonPrefix + @('-c', 'import sys; raise SystemExit(0 if sys.version_info[0] == 3 else 1)')
& $pythonCommand @pythonVersionArgs
if ($LASTEXITCODE -ne 0) { Fail 'Python 3 is required for safe archive extraction' }

$extractArgs = @() + $pythonPrefix + @($verifier, '--archive', $archive, '--format', 'zip', '--entry', $executable, '--extract-to', $runtime)
& $pythonCommand @extractArgs
if ($LASTEXITCODE -ne 0) { Fail 'CMF archive verification/extraction failed' }
$env:OLLAMA_CORTIQ_PATH = (Resolve-Path -LiteralPath (Join-Path $runtime $executable)).Path
Write-Output "verified Cortiq runtime: $env:OLLAMA_CORTIQ_PATH"
```

The procedure refuses to overwrite an existing `cortiq.exe`. Stop a prior
server and remove the isolated `.runtime` directory before repeating the setup.

The following completes the local import and API exercise. It is a documented
operator procedure, **not** evidence of a successful Windows adapter E2E run;
keep the status as reviewed-input-only until that test exists.

In the PowerShell window that ran the runtime setup, set isolated state and
start the server. Keep it open:

```powershell
$root = (Get-Location).Path
$env:OLLAMA_BIN = Join-Path $root 'ollamacmf.exe'
$env:OLLAMA_HOST = '127.0.0.1:11456'
$env:OLLAMA_MODELS = Join-Path $root '.ollamacmf-models'
& $env:OLLAMA_BIN serve
```

Open a second PowerShell window at the same repository checkout, point its CLI
at that isolated server, import the fixture, and make one ordinary Ollama API
request. The second window does not need the sidecar path because only the
server launches Cortiq:

```powershell
$root = (Get-Location).Path
$ollama = Join-Path $root 'ollamacmf.exe'
$env:OLLAMA_HOST = '127.0.0.1:11456'
$fixture = Join-Path $root 'testdata\cmf\tiny-v0.8.19.cmf'
$modelfile = Join-Path $env:TEMP 'Modelfile.cmf'
@"
FROM $fixture
PARAMETER temperature 0
PARAMETER num_predict 2
"@ | Set-Content -NoNewline -Path $modelfile

& $ollama create cmf-smoke -f $modelfile
Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:11456/api/chat' `
  -ContentType 'application/json' `
  -Body '{"model":"cmf-smoke","messages":[{"role":"user","content":"Hello"}],"stream":false,"options":{"num_predict":2}}'
```

The fixture has random tiny weights; a response would test the adapter path,
not model quality. Remove `cmf-smoke` while the server is still running, then
stop the server and remove `.ollamacmf-models` and `.runtime` only if they were
created for this exercise.

## Compatibility and limits

The adapter exposes a small, explicit subset of the normal Ollama surface. A
request outside it fails rather than being silently ignored.

| Area | Current CMF behavior |
| --- | --- |
| Text generation | `ollama run`, `/api/chat`, formatted `/api/generate`, and raw `/api/generate` are available as described above |
| Model defaults | Only `PARAMETER temperature` and `PARAMETER num_predict` |
| Chat request options | `temperature`, `top_p`, `num_predict` (`-1` default or non-negative), and `seed` (`-1` default or non-negative) |
| Raw completion options | `temperature` and `num_predict`; explicitly supplied `top_p` or `seed` are rejected |
| Output length | `num_predict` becomes Cortiq `max_tokens`; the `-1` Ollama default becomes a deliberate 256-token limit, while values below `-1` are rejected |
| Embeddings / token APIs / rendered templates | Not supported by this adapter |
| Images and other completion media | Rejected |
| Structured output and log probabilities | Rejected |
| Tools and positive thinking requests | Rejected because CMF models currently advertise completion capability only |
| Context controls | Explicit `truncate` and `shift` are rejected; Ollama has no token-accurate CMF context accounting |
| Registry transport | CMF pull and push are rejected |
| Model inspection | The fork does not invent GGUF tensor metadata or an Ollama template for the opaque blob |

These are **adapter** boundaries, not claims that Cortiq itself lacks every
underlying control. In particular, the fork intentionally maps only the listed
sampling fields. It must be extended and tested before exposing more of
Cortiq's protocol through Ollama.

Cortiq selects CPU/GPU execution independently. The fork cannot reliably
account for per-device CMF VRAM, so treat normal Ollama resource reporting as
non-authoritative for CMF. A loaded CMF runner is exclusive with other runners.
`CMF_SERVE_SLOTS` must be a positive integer when set. In normal server flow,
the scheduler supplies one CMF slot, so the adapter sets it to one unless it is
explicitly overridden.

## Runtime discovery and troubleshooting

### `CMF runtime is not available`

Set `OLLAMA_CORTIQ_PATH` to an absolute path for the executable from the
verified lock-selected archive and restart `ollamacmf serve`:

```sh
export OLLAMA_CORTIQ_PATH=/absolute/path/to/cortiq
```

An explicit path is authoritative: an empty, non-absolute, or non-executable
path fails rather than falling back. `OLLAMA_CORTIQ_ALLOW_PATH=1` enables
workspace/`PATH` discovery only for local development; it is not a deployment
mechanism.

### `exec format error` or the sidecar cannot start

Check the runtime target against `go env GOOS` and `go env GOARCH` for the
binary you built, then compare it with the selected `cmf.lock.json` asset. Do
not use an asset for another OS/architecture or an unreviewed source build as
a substitute. This matters when Go is running under an emulator: `uname` alone
may describe a different architecture from the fork binary.

### Model import succeeds but a request fails

Use a real runnable generative CMF v2 model. The initial importer deliberately
only validates the cheap container envelope; Cortiq performs full model loading.
Read server logs from the `ollamacmf serve` terminal and verify that the model
is not a skill or decision CMF.

### The CLI talks to the wrong server

Use a unique `OLLAMA_HOST` for the fork server and export the same value in the
CLI terminal. The README uses `127.0.0.1:11456` so an existing Ollama service
on the default port cannot be mistaken for this fork.

## Security boundary

The sidecar binds an ephemeral `127.0.0.1` port. Loopback is not an
authentication boundary: another process on the same host may connect. The
adapter reserves a port and then asks Cortiq to bind it, which leaves a local
TOCTOU window; it also cannot eliminate a bind/request race if the child exits.

Treat the entire path as trusted-local only:

- do not expose the sidecar port, reverse-proxy it, or pass it to an app;
- do not use it on shared workstations, multi-user systems, CI agents running
  untrusted code, or hostile workload hosts;
- do not rely on it to protect API credentials, prompts, tools, or model data;
- stop the server and remove the isolated model store after a smoke test if it
  held anything you do not want to retain.

A future authenticated/private runner protocol is required before this
restriction can be relaxed.
