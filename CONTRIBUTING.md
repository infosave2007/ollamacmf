# Contributing to ollamacmf

Thanks for helping improve `ollamacmf`. It is a fork of [Ollama](https://github.com/ollama/ollama) that adds support for CMF models through the Cortiq runtime, and it is maintained independently of Ollama, Inc. Please open fork-specific issues and pull requests in this repository, not upstream.

## Before you start

- Read the [user guide](docs/cmf.md) to see how things are supposed to behave.
- Read the [maintainer notes](docs/cmf-engine.md) if you plan to touch the adapter (`cmfrunner/`, `server/cmf_*.go`, `server/create.go`, `server/sched.go`) or the runtime lock.
- For anything larger than a bug fix, open an issue first so the design can be discussed.

## Building and testing

```sh
go build -o ollamacmf .
go test ./cmfrunner ./server ./cmd
```

`go build` alone produces a binary that serves CMF models. The native GGUF and safetensors runners are built as described in [docs/development.md](docs/development.md) and are not needed for CMF work.

To test against the real runtime, let the server install it (`./ollamacmf cmf setup`) and run a small model:

```sh
./ollamacmf serve &
./ollamacmf run hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp "hello"
```

## Guidelines

- Keep the user experience simple. A new option should have a sensible default, and an unsupported request option should be ignored (with a debug log) rather than rejected, unless ignoring it would silently produce wrong results.
- Changes to what the adapter forwards to Cortiq need tests in `cmfrunner/client_test.go` and, for the HTTP layer, in `server/cmf_test.go`.
- Changes to the runtime lock go through [runtime updates](docs/cmf-release-automation.md). Never point the lock at `latest` or at an archive for a different platform.
- Keep upstream Ollama code changes minimal and separate from fork features where practical, so rebases onto upstream stay reviewable. Bugs in unmodified upstream code are best reported to [ollama/ollama](https://github.com/ollama/ollama) as well.
- Document user-visible behaviour in the README and the user guide in the same pull request.

## Pull requests

Describe the problem, the user-visible change, and how you verified it. Use commit titles of the form `area: short description`, for example `cmfrunner: forward presence_penalty`.

## Security

Do not report vulnerabilities in public issues; see [SECURITY.md](SECURITY.md).
