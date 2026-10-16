# ollamacmf

> **Experimental, source-only Ollama fork that runs locally imported CMF v2 text
> models through a Cortiq sidecar.**

[CMF quick start](docs/cmf.md) · [compatibility and safety](docs/cmf.md#compatibility-and-limits) · [runtime intake](docs/cmf-release-automation.md) · [development](docs/development.md) · [upstream Ollama](https://github.com/ollama/ollama)

`ollamacmf` is an independent fork of [Ollama](https://github.com/ollama/ollama).
It is not affiliated with Ollama, Inc.  The fork keeps Ollama's local model
store and public API, then starts [Cortiq](https://github.com/infosave2007/cmf)
as a local subprocess for a runnable CMF model.

> [!WARNING]
> This is a bootstrap integration for a **trusted, single-user host**. Cortiq's
> sidecar uses unauthenticated loopback HTTP, and the current launch sequence
> has a local port bind race. Do not expose the sidecar, use it on a multi-user
> or hostile-workload host, or treat loopback as an authorization boundary.
> Read the [security boundary](docs/cmf.md#security-boundary) before importing a
> model.

## Status at a glance

| Area | Current status |
| --- | --- |
| CMF model path | Local `FROM /absolute/path/model.cmf` import for runnable generative CMF v2 text models |
| Runtime | An explicitly configured, build-target-matched Cortiq executable; **not** embedded or bundled |
| Distribution | Source checkout only; no CMF-enabled official binaries, installer, or Docker image |
| Registry | CMF `ollama pull` and `ollama push` are intentionally rejected |
| Automation | A scheduled/repository-dispatch workflow proposes a verified **candidate lock PR**; it does not package, sign, or publish a release |
| Platform evidence | Linux `amd64` adapter E2E runs in CI; other locked desktop artifacts are reviewed inputs, not a production-support promise |

The [canonical lock](cmf.lock.json) records the exact Cortiq release assets
reviewed by this revision. It is an integrity record, not a cryptographic
provenance attestation: the currently pinned upstream tag is unsigned and its
GitHub release was not immutable when checked. Never substitute `latest`, a
random `PATH` binary, or an artifact for a different OS/architecture.

## What the fork adds

```text
Ollama CLI / local Ollama API
             |
             v
      ollamacmf server
  (model store + scheduling)
             |
             v
  cmfrunner -> Cortiq subprocess -> local .cmf model
      127.0.0.1:<ephemeral port>
```

Clients continue to call the normal Ollama endpoint. The ephemeral Cortiq port
is an implementation detail and must not be used by clients:

- `ollama run`, `POST /api/chat`, and formatted `POST /api/generate` use
  Cortiq's chat-completions endpoint so Cortiq owns the model tokenizer and
  chat template.
- `POST /api/generate` with `"raw": true` uses Cortiq's completion endpoint.
- Ollama stores the CMF blob as an opaque model layer. It performs inexpensive
  CMF-envelope checks at import time; Cortiq remains responsible for the full
  model and weights validation.

## Verified local smoke test (macOS/Linux)

This is deliberately a source-build flow. **Do not use an official Ollama
installer or image for these steps**: those distributions do not contain this
fork's CMF adapter.

### 1. Build this checkout

Prerequisites: Git, Go, `curl`, `jq`, Python 3, and either `shasum` or
`sha256sum`. Build for the same OS/architecture as the Cortiq runtime. The
selector below uses the Go build target, not `uname`, so it also remains correct
when Go runs under an emulator.

```sh
git clone https://github.com/infosave2007/ollamacmf.git
cd ollamacmf
go build -o ./ollamacmf .
export OLLAMA_BIN="$(pwd)/ollamacmf"
"$OLLAMA_BIN" --version
```

`go build` is sufficient for the CMF smoke flow below. Build the full native
Ollama payload with the [development guide](docs/development.md) when working
on the other Ollama engines.

### 2. Download the *locked* runtime, verify it, then extract it

The POSIX-`sh` snippet selects exactly one artifact for the local Go build
target from this checkout's `cmf.lock.json`; it fails rather than substituting
a target. Before extraction, it checks each downloaded file's size and SHA-256
**directly against the lock**, checks that the pinned checksum sidecar names the
same archive hash, and uses the tracked safe archive verifier. It downloads but
never executes the runtime during setup.

```sh
set -eu

fail() {
  printf '%s\n' "error: $*" >&2
  exit 1
}

sha256_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk 'NR == 1 { print $1 }'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk 'NR == 1 { print $1 }'
  else
    fail 'need shasum or sha256sum'
  fi
}

file_size() {
  wc -c < "$1" | tr -d '[:space:]'
}

require_sha256() {
  value=$1
  label=$2
  case "$value" in
    ''|*[!0123456789abcdef]*) fail "$label is not a lowercase SHA-256" ;;
  esac
  [ "${#value}" -eq 64 ] || fail "$label is not a 64-character SHA-256"
}

require_positive_decimal() {
  value=$1
  label=$2
  case "$value" in
    ''|*[!0123456789]*) fail "$label is not a positive decimal byte count" ;;
  esac
  [ "$value" -gt 0 ] 2>/dev/null || fail "$label is not a positive decimal byte count"
}

require_basename() {
  value=$1
  label=$2
  case "$value" in
    ''|.|..|*/*|*\\*|*[!A-Za-z0-9._-]*) fail "$label is not a safe file name" ;;
  esac
}

repo_root=$(pwd -P)
[ -f "$repo_root/cmf.lock.json" ] || fail 'run this from the checkout root'
verifier="$repo_root/.github/scripts/verify_cmf_runtime_archive.py"
[ -f "$verifier" ] || fail 'CMF archive verifier is missing'
command -v python3 >/dev/null 2>&1 || fail 'python3 is required for safe archive extraction'

case "$(go env GOOS)" in
  linux|darwin) cmf_os=$(go env GOOS) ;;
  *) fail 'use the Windows guide or an explicitly reviewed build target' ;;
esac
case "$(go env GOARCH)" in
  amd64|arm64) cmf_arch=$(go env GOARCH) ;;
  *) fail "unsupported Go build architecture: $(go env GOARCH)" ;;
esac
cmf_platform="$cmf_os/$cmf_arch"

asset=$(jq -ce --arg platform "$cmf_platform" '
  [.artifacts[] | select(.platform == $platform)] as $matches
  | if ($matches | length) == 1 then $matches[0]
    else error("expected exactly one reviewed artifact for \($platform); found \($matches | length)")
    end
' "$repo_root/cmf.lock.json") || fail "could not select a unique reviewed runtime for $cmf_platform"

asset_field() {
  printf '%s\n' "$asset" | jq -er "$1"
}

archive_url=$(asset_field '.archive.url') || fail 'lock has no archive URL'
archive_name=$(asset_field '.archive.name') || fail 'lock has no archive name'
archive_size=$(asset_field '.archive.sizeBytes') || fail 'lock has no archive byte count'
archive_sha256=$(asset_field '.archive.sha256') || fail 'lock has no archive SHA-256'
archive_format=$(asset_field '.archive.format') || fail 'lock has no archive format'
archive_entry=$(asset_field '.archive.entries | if type == "array" and length == 1 then .[0] else error("expected one archive entry") end') || fail 'lock has invalid archive entries'
checksum_url=$(asset_field '.checksum.url') || fail 'lock has no checksum URL'
checksum_name=$(asset_field '.checksum.name') || fail 'lock has no checksum name'
checksum_size=$(asset_field '.checksum.sizeBytes') || fail 'lock has no checksum byte count'
checksum_sha256=$(asset_field '.checksum.sha256') || fail 'lock has no checksum SHA-256'
executable=$(asset_field '.installedExecutable') || fail 'lock has no installed executable name'

require_basename "$archive_name" 'archive name'
require_basename "$checksum_name" 'checksum name'
require_basename "$archive_entry" 'archive entry'
require_basename "$executable" 'installed executable'
require_positive_decimal "$archive_size" 'archive size'
require_positive_decimal "$checksum_size" 'checksum size'
require_sha256 "$archive_sha256" 'locked archive SHA-256'
require_sha256 "$checksum_sha256" 'locked checksum SHA-256'
[ "$archive_entry" = "$executable" ] || fail 'lock archive entry and installed executable disagree'
case "$archive_format" in tar.gz) ;; *) fail "unsupported Unix archive format: $archive_format" ;; esac
case "$archive_url" in https://*) ;; *) fail 'archive URL must use HTTPS' ;; esac
case "$checksum_url" in https://*) ;; *) fail 'checksum URL must use HTTPS' ;; esac

runtime_dir="$repo_root/.runtime/cortiq"
mkdir -p "$runtime_dir"
[ -d "$runtime_dir" ] || fail 'runtime path is not a directory'
[ ! -e "$runtime_dir/$executable" ] || fail "refusing to overwrite existing $runtime_dir/$executable"
archive_path="$runtime_dir/$archive_name"
checksum_path="$runtime_dir/$checksum_name"

curl --fail --location --proto '=https' --proto-redir '=https' \
  --output "$archive_path" "$archive_url"
[ "$(file_size "$archive_path")" = "$archive_size" ] || fail 'downloaded archive size differs from lock'
actual_archive_sha256=$(sha256_file "$archive_path") || fail 'could not hash downloaded archive'
require_sha256 "$actual_archive_sha256" 'downloaded archive SHA-256'
[ "$actual_archive_sha256" = "$archive_sha256" ] || fail 'downloaded archive SHA-256 differs from lock'

curl --fail --location --proto '=https' --proto-redir '=https' \
  --output "$checksum_path" "$checksum_url"
[ "$(file_size "$checksum_path")" = "$checksum_size" ] || fail 'downloaded checksum size differs from lock'
actual_checksum_sha256=$(sha256_file "$checksum_path") || fail 'could not hash downloaded checksum asset'
require_sha256 "$actual_checksum_sha256" 'downloaded checksum asset SHA-256'
[ "$actual_checksum_sha256" = "$checksum_sha256" ] || fail 'downloaded checksum asset SHA-256 differs from lock'

checksum_fields=$(awk 'NR == 1 { fields = NF } END { if (NR != 1) exit 1; print fields }' "$checksum_path") || fail 'checksum asset must contain exactly one line'
[ "$checksum_fields" = 2 ] || fail 'checksum asset must contain exactly a digest and file name'
sidecar_archive_sha256=$(awk 'NR == 1 { print $1; exit }' "$checksum_path" | tr '[:upper:]' '[:lower:]')
sidecar_archive_name=$(awk 'NR == 1 { name = $2; sub(/\r$/, "", name); print name; exit }' "$checksum_path")
case "$sidecar_archive_name" in \*) sidecar_archive_name=${sidecar_archive_name#\*} ;; esac
require_sha256 "$sidecar_archive_sha256" 'release checksum digest'
[ "$sidecar_archive_name" = "$archive_name" ] || fail 'release checksum names a different archive'
[ "$sidecar_archive_sha256" = "$archive_sha256" ] || fail 'release checksum disagrees with the lock'

python3 "$verifier" --archive "$archive_path" --format "$archive_format" \
  --entry "$executable" --extract-to "$runtime_dir"
export OLLAMA_CORTIQ_PATH="$runtime_dir/$executable"
test -x "$OLLAMA_CORTIQ_PATH"
printf 'verified Cortiq runtime: %s\n' "$OLLAMA_CORTIQ_PATH"
```

The lock currently has Unix artifacts for Linux `amd64`, macOS `amd64`, and
macOS `arm64`. Linux `arm64` is deliberately rejected because the pinned CMF
release has no reviewed runtime for that build target. See the
[platform matrix](docs/cmf.md#platform-and-runtime-scope) for Windows and
evidence boundaries.

### 3. Start an isolated fork server

Use a distinct port and model directory so the test cannot accidentally talk to
an already-running official Ollama server or alter its model store.

```sh
export OLLAMA_HOST=127.0.0.1:11456
export OLLAMA_MODELS="$(pwd)/.ollamacmf-models"
"$OLLAMA_BIN" serve
```

Keep that terminal open. `OLLAMA_CORTIQ_PATH` is read by the server process, so
set it **before** starting the server.

### 4. Import the checked-in test fixture and call the Ollama API

In a second terminal in the same checkout:

```sh
export OLLAMA_BIN="$(pwd)/ollamacmf"
export OLLAMA_HOST=127.0.0.1:11456
fixture="$(pwd)/testdata/cmf/tiny-v0.8.19.cmf"
printf 'FROM %s\nPARAMETER temperature 0\nPARAMETER num_predict 2\n' "$fixture" \
  > /tmp/Modelfile.cmf

"$OLLAMA_BIN" create cmf-smoke -f /tmp/Modelfile.cmf
curl --fail --silent http://127.0.0.1:11456/api/chat \
  -H 'Content-Type: application/json' \
  -d '{"model":"cmf-smoke","messages":[{"role":"user","content":"Hello"}],"stream":false,"options":{"num_predict":2}}'
printf '\n'
```

The fixture is intentionally tiny with random weights: a successful response
checks the integration contract, **not model quality**. Replace its absolute
path in the Modelfile with a trusted, runnable CMF v2 generative text model for
real use; see [obtaining a CMF model](docs/cmf.md#obtaining-a-cmf-model) for
the separate upstream conversion/model guidance and its boundaries. To clean
up, run `"$OLLAMA_BIN" rm cmf-smoke` while the server is
still available, then stop the server and remove `.ollamacmf-models` and
`.runtime` if they were created only for the smoke test.

For a Windows PowerShell flow, a real-model example, troubleshooting, and the
full compatibility matrix, start with the [CMF/Cortiq guide](docs/cmf.md).

## Compatibility and intentional boundaries

CMF is **not** a generic replacement for every Ollama feature. Current CMF
models advertise completion capability only. The fork rejects embeddings,
tokenization/detokenization, rendered templates, images/media, structured
output, log probabilities, tool calls, and positive thinking requests rather
than silently ignoring them. Model `PARAMETER` accepts only `temperature` and
`num_predict`; request options are a deliberately smaller adapter surface than
Cortiq's own HTTP API. See exact behavior and known resource limits in
[CMF/Cortiq guide](docs/cmf.md#compatibility-and-limits).

The registered-runtime path is also intentionally narrow:

- A CMF model is imported locally from one absolute file; it is not pulled from
  or pushed to a registry.
- `OLLAMA_CORTIQ_PATH` must be an absolute executable path. The optional
  `OLLAMA_CORTIQ_ALLOW_PATH=1` fallback is for local development only.
- The current adapter has no reliable CMF GPU-memory accounting. CMF loading is
  exclusive with other runners; in normal server flow the scheduler supplies
  one slot, so `CMF_SERVE_SLOTS` is one unless an explicit positive value is
  supplied.

## Runtime intake and release policy

[`Intake verified CMF runtime update`](.github/workflows/cmf-release-sync.yml)
runs on a four-hour reconciliation schedule (`17 */4 * * *`) or a
`cmf-release` repository dispatch. It validates a complete, bounded set of
upstream release assets and checksums, then opens a create-only candidate branch
and PR that change only `cmf.lock.json`.

That is **candidate intake, not automatic binary publishing**. The workflow
never embeds Cortiq into an Ollama payload, publishes a Docker image, tags an
`ollamacmf` release, or changes a `latest` release. A separate protected
promotion/publishing pipeline is still required after human review, runtime
E2E, license/provenance review, and platform signing/notarization. The
[automation runbook](docs/cmf-release-automation.md) describes the exact
current behavior and the required next gate without claiming an unimplemented
release system.

## Documentation map

| Document | Use it for |
| --- | --- |
| [CMF/Cortiq guide](docs/cmf.md) | First install, import, requests, compatibility, security, and troubleshooting |
| [CMF engine contract](docs/cmf-engine.md) | Maintainer-facing runner, lock, and packaging invariants |
| [CMF release automation](docs/cmf-release-automation.md) | Candidate intake workflow and what it deliberately does not publish |
| [Development guide](docs/development.md) | Native Ollama build prerequisites and CI |
| [Base Ollama documentation](https://docs.ollama.com) | General upstream behavior; it does **not** document this CMF adapter |

## Contributing, security, and upstream

Please report fork bugs and feature proposals in the
[ollamacmf issue tracker](https://github.com/infosave2007/ollamacmf/issues) and
read [CONTRIBUTING.md](CONTRIBUTING.md). Report security issues privately using
the [fork's GitHub Security Advisories](https://github.com/infosave2007/ollamacmf/security/advisories/new);
do not send fork-specific reports to Ollama, Inc.

This project is distributed under the [MIT License](LICENSE) inherited from
Ollama. The reviewed Cortiq runtime inputs and the test fixture are recorded as
Apache-2.0 in [cmf.lock.json](cmf.lock.json). Keep the relevant third-party
notices with any future runtime distribution.
