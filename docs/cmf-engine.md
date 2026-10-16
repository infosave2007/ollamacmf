# CMF engine: experimental bootstrap and packaging contract

This independent fork of Ollama contains an **experimental** adapter for
[CMF](https://github.com/infosave2007/cmf) / Cortiq. It imports one opaque
`.cmf` model blob, starts Cortiq as a sidecar, and keeps Ollama responsible for
its model store and public API. [`../cmf.lock.json`](../cmf.lock.json) is the
**sole canonical lock** for the reviewed Cortiq desktop runtime inputs and the
matching, reviewed runnable E2E fixture.

This is not a public product release, installer, Docker image, or replacement
for an official Ollama distribution. No Cortiq binary is bundled yet. Treat the
adapter as a local experimental integration, not production support.

## Security and deployment boundary

> [!WARNING]
> The Cortiq sidecar currently serves **unauthenticated HTTP** on a loopback
> port. Loopback is a routing choice, **not** an authentication or authorization
> boundary: another local process can connect to it. In addition, the adapter
> first reserves an ephemeral port and then starts Cortiq, leaving a local
> time-of-check/time-of-use race before Cortiq binds it (and cannot fully rule
> out a bind/request race if the sidecar exits). Use this bootstrap only
> on a trusted, single-user host. Do not use it on multi-user machines, hostile
> workload hosts, or with prompts, tools, or model data that require local
> process isolation. Do not expose the sidecar port or rely on it to protect
> secrets. A future CMF authenticated/private runner protocol is required before
> this restriction can be relaxed.

The sidecar is an implementation detail. Ollama invokes Cortiq on
`127.0.0.1` through `GET /healthz`, `POST /v1/completions`, and
`POST /v1/chat/completions`; their ports are ephemeral and must not be
hard-coded. Clients should use the normal Ollama server API instead, beginning
with `POST /api/generate` (and the experimental native `POST /api/chat` path
for text messages), on the Ollama server endpoint.

## Experimental local use

Use a build of **this fork**; there is no packaged CMF-enabled release yet.
The server process must be able to execute a Cortiq binary matching the host
and the CMF model must be a real CMF v2 file: the adapter checks its fixed
128-byte envelope, version, required features, runnable model kind, and section
bounds before it writes a manifest. Cortiq remains the authority for validating
the opaque model header, tensor directory, and weights.

1. Obtain the host's Cortiq archive named in [`../cmf.lock.json`](../cmf.lock.json),
   verify it as described in [Verification procedure](#verification-procedure),
   and extract it without running it. Put the executable in a user-controlled
   location; on Unix, preserve its executable bit. For example, after verified
   extraction:

   ```sh
   mkdir -p "$HOME/.local/lib/ollamacmf"
   cp /absolute/path/to/extracted/cortiq "$HOME/.local/lib/ollamacmf/cortiq"
   chmod 0755 "$HOME/.local/lib/ollamacmf/cortiq"
   export OLLAMA_CORTIQ_PATH="$HOME/.local/lib/ollamacmf/cortiq"
   test -x "$OLLAMA_CORTIQ_PATH"
   ```

   On Windows, point `OLLAMA_CORTIQ_PATH` at the verified `cortiq.exe`. The
   explicit variable is authoritative and must be an absolute executable path:
   if it is empty or invalid, the adapter fails rather than falling back to
   another binary. Normal runtime lookup considers only the packaged location
   beside Ollama. For local development only, an operator can explicitly opt
   into workspace/`PATH` lookup with `OLLAMA_CORTIQ_ALLOW_PATH=1`; it is never
   enabled by default. Set configuration in the environment that launches
   `ollama serve`, then restart that server if it was already running.

2. Create a Modelfile that names one absolute CMF path, then import it with the
   standard CLI. This is the supported local-import path; it does not use a
   remote registry. `ollama pull` intentionally rejects registry manifests
   that declare CMF, including a CMF `model_format` hidden in an otherwise
   ordinary manifest, before the model becomes locally visible. `ollama push`
   likewise refuses CMF manifests before it starts an upload: this bootstrap
   has no CMF registry transport or remote-envelope validation path.

   ```sh
   cat >/tmp/Modelfile.cmf <<'EOF'
   FROM /absolute/path/to/model.cmf
   EOF

   # In one terminal, with OLLAMA_CORTIQ_PATH exported:
   ollama serve

   # In another terminal, using this fork's ollama client:
   ollama create local-cmf -f /tmp/Modelfile.cmf
   ollama run local-cmf
   ```

   The CMF import accepts exactly one CMF model file. It rejects a mixed or
   multi-file import, draft model, create-time quantization, Ollama
   `TEMPLATE`/`RENDERER`/`PARSER`, image messages, and unsupported parameters.
   These restrictions are reapplied to inherited Modelfile layers when deriving
   a CMF model with `FROM`; the base must have exactly one model layer and only
   safe system/license/validated parameter/message layers. An invalid
   legacy/manual base fails at create time instead of producing a model that
   later requests cannot run. `ollama show`
   intentionally does not invent GGUF tensor metadata or emit an ignored Ollama
   template for an opaque CMF blob.

3. Start with ordinary text generation. The adapter maps completion requests to
   Cortiq's `/v1/completions` endpoint. Message-shaped requests use Cortiq's
   native chat endpoint, so Ollama does not attempt to reproduce the CMF
   tokenizer or chat template.

### Current feature and resource limits

The adapter deliberately rejects controls that Cortiq v0.8.19 does not expose
through its public HTTP protocol rather than silently ignoring them:

- embeddings; tokenization, detokenization, and rendered chat templates;
- images and other completion media;
- constrained/structured output (`format`) and log probabilities;
- tool use and positive thinking requests. Cortiq can report these capabilities
  only after it has loaded a particular model/template, but Ollama must validate
  capabilities before it starts a sidecar. The bootstrap therefore advertises
  completion only rather than accepting controls that may be ignored;
- non-default controls such as `top_k`, `min_p`, repetition/presence/frequency
  penalties, `stop`, and `num_keep` (completion also rejects non-default
  `top_p` and `seed`). A raw `/api/generate` completion rejects *any*
  explicitly supplied `top_p` or `seed`, including an Ollama-default value,
  because Cortiq's completion endpoint exposes neither control; native chat
  requests use Cortiq's separate chat protocol instead;
- explicit `truncate` or `shift`. Cortiq owns context policy and does not
  expose Ollama-compatible token accounting, truncation, or context shifting,
  so the adapter rejects an explicit request rather than silently ignoring it.

`num_predict` maps to Cortiq `max_tokens`. Ollama's usual unbounded default
(`num_predict=-1`) has no equivalent in Cortiq's public protocol, so this
adapter sends an explicit, stable limit of **256** tokens rather than relying on
a mutable sidecar default. Set a finite `num_predict` when a different output
budget is required.

CMF owns its context window and chat formatting. The current Cortiq protocol
has no metadata/configuration operation through which this adapter can enforce
or report Ollama's `num_ctx`, so the effective context limit is unknown to
Ollama. It does not perform token-accurate truncation; oversized requests are
owned by Cortiq and may fail or be handled differently from a GGUF runner.

Cortiq also independently selects CPU/GPU execution. The bootstrap cannot
reliably report per-GPU VRAM or device placement to Ollama's scheduler, so do
not treat Ollama's normal resource accounting as an enforced CMF limit. Keep
concurrency conservative on a trusted host. Until that accounting exists, CMF
runner residency is exclusive: loading a CMF model unloads every other Ollama
runner, and loading another model unloads the CMF runner.
`CMF_SERVE_SLOTS`, if explicitly set for the Cortiq process, must be a positive
integer and remains
authoritative; otherwise the adapter sets it to one so Cortiq does not apply
its multi-slot default. The sidecar receives a restricted runtime environment
(CMF/GPU/loader/locale controls, not arbitrary Ollama API credentials), so put
only intentional Cortiq tuning values under the `CMF_` namespace.

## Canonical lock and release policy

The initial work is deliberately a bootstrap:

- A CMF update can create a **candidate intake PR** containing a reviewed
  update to `cmf.lock.json`.
- A candidate is not a public `ollamacmf` release, installer, Docker image, or
  replacement for an official Ollama distribution.
- Public promotion remains a separate, protected decision after engine tests,
  platform smoke tests, licensing review, signing/notarization, and a
  provenance review are complete.

In particular, an upstream CMF release must never silently mutate an existing
public `ollamacmf` release or a `latest` tag.

The `Intake verified CMF runtime update` workflow is triggered by a
`cmf-release` repository dispatch or a periodic reconciliation. Its read-only
intake job independently queries the GitHub Releases and annotated-tag APIs,
requires every reviewed desktop asset and adjacent checksum, and validates
archive metadata without extracting or executing payloads. A separate
write-scoped job then schema-validates that bounded candidate artifact against
the checked-out fixture and creates a tag-and-source-commit-named branch with a
zero-OID, create-only lease. It never updates or overwrites a candidate branch.
Only after that conditional branch creation does it open a candidate PR. It
does **not** build or publish a product.

The unprivileged `Verify CMF candidate inputs` PR workflow rechecks the exact
locked release inputs and then runs the reviewed tiny fixture through the
verified Linux `amd64` Cortiq archive **via the Go adapter**. When intake
dispatches it, trusted default-branch code first requires a lowercase 40-hex
commit, proves that it is a one-parent, default-branch-descended commit changing
only `cmf.lock.json`, and binds it to the tag-and-source-commit candidate branch
before checking it out. Candidate data and trusted default-branch sources
are checked out into separate sibling directories, so checkout cleanup cannot
replace the trusted verifier tree. The candidate checkout is then data only:
trusted default-branch verifier and adapter code process its lock and fixture.
A normal pull request keeps the ordinary read-only candidate-code path, but candidate
Python/verifier and runner subprocesses are invoked with `env -i`; its API
verification uses no explicitly supplied `GH_TOKEN`. The runtime job has only
`contents: read` for checkout, no persisted credentials, and executes the
sidecar under `env -i`; no token reaches the downloaded process and it never
runs in the write-token intake job. Missing assets during upstream upload leave
the lock unchanged for a later retry; malformed, duplicate, mismatched,
downgraded, or mutable same-tag inputs fail.

The write-capable intake workflow intentionally has no manual-dispatch
trigger: scheduled and repository-dispatch runs always resolve trusted
default-branch code. The separate smoke workflow may be dispatched manually
for an immutable candidate SHA, but it uses only public, unauthenticated API
reads. Candidate PR creation and the automatic trusted validation dispatch
require the repository's `GITHUB_TOKEN` policy to allow `contents: write`,
`pull-requests: write`, and `actions: write`; no long-lived secret is supplied
to archive verification or to Cortiq.

## Pinned upstream release

The canonical lock currently pins
[`infosave2007/cmf` v0.8.19](https://github.com/infosave2007/cmf/releases/tag/v0.8.19):

| Field | Value |
| --- | --- |
| Release ID | `414030103` |
| Published | `2026-10-15T16:28:20Z` |
| Annotated tag object | `4589a24bb220c5ae8839f2eb1d814ac6a0ffe0e5` |
| Resolved source commit | `5d6f1830cfec6ed1cdd1eca9e9c259a66c998716` |
| Tag signature | unsigned |
| GitHub release immutability | `false` at validation time |

The lock stores the archive URL, GitHub release-asset ID, byte count, checksum
asset, SHA-256 values for both assets, archive format, and expected executable
entry for each reviewed platform. It also pins a real fixture's path, byte
count, SHA-256, source commit, and runtime tag. No job or installer may use an
unpinned `latest` release, branch, tag, or source checkout as a fallback.

## Runnable E2E fixture

[`../testdata/cmf/tiny-v0.8.19.cmf`](../testdata/cmf/tiny-v0.8.19.cmf) is a
small, complete CPU-runnable generative CMF v2 model. It is deliberately not
the synthetic envelope used by Go unit tests. Its checked-in provenance records
the exact `infosave2007/cmf` source commit, generator-script hashes, generation
commands, toolchain record, artifact SHA-256, license, and validation contract:
[`../testdata/cmf/tiny-v0.8.19.provenance.json`](../testdata/cmf/tiny-v0.8.19.provenance.json).

This fixture was manually exercised on the pinned macOS arm64 runtime through
`serve`, `/healthz`, completion, and streamed chat. CI repeats the stronger
adapter-level E2E with the exact Linux amd64 archive. It does not assert model
text, only protocol completion and cleanup; the fixture's tiny random weights
are for contract testing, not model quality.

The intake workflow never regenerates this artifact. A future CMF runtime tag
will initially retain a mismatched fixture record, causing candidate CI to fail
with an explicit message. A reviewer must deliberately regenerate, hash,
license-review, and validate a matching fixture before merging the lock update.
This prevents a release event from silently replacing either runtime bytes or
model-test bytes.

## Initial platform scope

The table describes the intended **first packaging scope**. It is a packaging
contract, not a declaration of production CMF support: a platform becomes
supported only after routing and platform testing are complete.

| Host platform | Upstream Cortiq target | Pinned archive | Archive entry | Scope |
| --- | --- | --- | --- | --- |
| Linux `amd64` | `x86_64-unknown-linux-gnu` | `cortiq-x86_64-unknown-linux-gnu.tar.gz` | `cortiq` | Initial packaging scope |
| macOS `arm64` | `aarch64-apple-darwin` | `cortiq-aarch64-apple-darwin.tar.gz` | `cortiq` | Initial packaging scope |
| macOS `amd64` | `x86_64-apple-darwin` | `cortiq-x86_64-apple-darwin.tar.gz` | `cortiq` | Initial packaging scope |
| Windows `amd64` | `x86_64-pc-windows-msvc` | `cortiq-x86_64-pc-windows-msvc.zip` | `cortiq.exe` | Initial packaging scope |
| Windows `arm64` | `aarch64-pc-windows-msvc` | `cortiq-aarch64-pc-windows-msvc.zip` | `cortiq.exe` | Initial packaging scope |
| Linux `arm64` | — | — | — | **Unsupported**: CMF v0.8.19 publishes no `aarch64-unknown-linux-gnu` Cortiq archive. |

CMF v0.8.19 also contains Android CLI and mobile FFI artifacts. They are not
part of this desktop/server runner contract and remain out of scope. A missing
platform must fail clearly; it must not receive an archive built for another
target or an unreviewed source build.

## Package layout and extraction rules

The exact final distribution layout is owned by a future packaging integration,
but a package built from this lock must preserve these invariants:

1. Install exactly the lock-listed executable for the matching host target:
   `cortiq` on Unix and `cortiq.exe` on Windows.
2. Keep the executable in a CMF-specific runner payload location, separate from
   llama.cpp, MLX, and other engine payloads. Do not overwrite or replace an
   existing runner.
3. On Unix, retain executable permissions; on Windows, retain the `.exe`
   filename. Do not run the downloaded executable during extraction.
4. Reject an archive whose entries differ from the lock's expected entry list,
   whose checksum does not match, whose only member is a link, special file,
   non-executable, or oversized payload, or whose platform does not match the
   running host. Use the tracked metadata verifier rather than `extractall`.
5. Include the CMF Apache-2.0 license and required notices with any distributed
   third-party runner payload.

A package may contain a generated manifest, but it must retain the upstream
release tag, resolved commit, archive SHA-256, and the `ollamacmf` source
revision so a shipped runner can be traced back to its inputs.

## Verification procedure

Before an artifact enters a candidate build, download the archive and its
adjacent `.sha256` file from the *same pinned release*. Verify the checksum
before extraction. For a Unix target, from a disposable directory:

```sh
curl --fail --location --retry 3 --retry-all-errors \
  -O https://github.com/infosave2007/cmf/releases/download/v0.8.19/cortiq-x86_64-unknown-linux-gnu.tar.gz
curl --fail --location --retry 3 --retry-all-errors \
  -O https://github.com/infosave2007/cmf/releases/download/v0.8.19/cortiq-x86_64-unknown-linux-gnu.tar.gz.sha256
shasum -a 256 -c cortiq-x86_64-unknown-linux-gnu.tar.gz.sha256
tar -tzf cortiq-x86_64-unknown-linux-gnu.tar.gz
```

For Windows, compare the expected hexadecimal value from the `.sha256` file
with `Get-FileHash -Algorithm SHA256` before expanding the ZIP. Do not use
`curl | tar`, `Invoke-Expression`, or a mutable redirect such as `latest`.

The v0.8.19 lock was independently checked by downloading all five listed
desktop archives, validating each against its release-provided SHA-256 file,
and listing their contents. The results are captured in `cmf.lock.json`.

## Update contract

A lock update is a reviewed source change, not an implicit runtime update. For
each new CMF release, the intake workflow must:

1. Select a non-draft, non-prerelease semantic-version release with an
   annotated tag that points directly to a commit, and record the tag object,
   verification state, and commit SHA.
2. Require the complete desktop asset set for every platform in the current
   scope, plus an adjacent checksum asset for each archive.
3. Independently download and validate all selected archives, sizes, checksum
   files, GitHub asset digests when available, and strict single-executable
   archive metadata (no link or special-file extraction).
4. Update only `cmf.lock.json`, preserve the existing fixture record, run engine
   adapter tests and the candidate-input verification workflow. A new tag cannot
   pass until a matching real fixture and its provenance are deliberately
   reviewed; intake never generates that fixture automatically.
5. Leave unsupported platforms unsupported until a reviewed lock revision adds
   a native artifact and its test coverage.

A newly appearing native Linux `arm64` archive intentionally fails automatic
intake rather than silently widening scope. The lock schema, platform contract,
and tests must be deliberately reviewed together.

## Versioning and trust boundary

When public releases are eventually approved, use a product version that names
both inputs, for example `v0.40.5-cmf.0.8.19`. The release manifest must also
record immutable Ollama-fork and CMF source SHAs. Synchronizing the Ollama
upstream is a separate reviewed operation and must not be coupled to a CMF
release event.

The current release checksums establish download integrity only. They do not
by themselves establish publisher provenance: the pinned CMF tag is unsigned
and its GitHub release was not immutable when validated. Before unattended
public promotion, require a verified upstream provenance mechanism (for
example, a GitHub artifact attestation or a verified Cosign signature), a
protected release environment, and pinned/locked CI dependencies.
