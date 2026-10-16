# Security policy

`ollamacmf` is an independent fork of [Ollama](https://github.com/ollama/ollama). This policy covers the code in this repository: the CMF adapter, the runtime installer, the Hugging Face pull path and the CI and release workflows.

## Reporting a vulnerability

Please do not open a public issue for a suspected vulnerability. Use the private [GitHub Security Advisories form](https://github.com/infosave2007/ollamacmf/security/advisories/new) and include:

- the `ollamacmf` version (`ollamacmf --version`) and OS/architecture;
- whether a specific CMF model, Cortiq release or workflow is involved;
- steps to reproduce and the impact you see.

Vulnerabilities in unmodified upstream Ollama code should also be reported to the [upstream security policy](https://github.com/ollama/ollama/blob/main/SECURITY.md). Vulnerabilities in the Cortiq runtime itself belong to [infosave2007/cmf](https://github.com/infosave2007/cmf/security).

## How ollamacmf protects what it downloads

- The Cortiq runtime is downloaded only from the release recorded in `cmfrunner/cmf.lock.json`, and the archive's size and SHA-256 are checked against the lock before it is unpacked. Only the single named executable is extracted.
- Model files pulled from Hugging Face are verified against the SHA-256 that Hugging Face publishes for the file before they are registered.
- Release archives are published with a `checksums.txt`; the installers verify the archive against it.

## Deployment notes

Cortiq runs as a child process of the server and listens on an ephemeral `127.0.0.1` port without authentication, the same way Ollama's own runners do. Any process on the same machine can reach it. Run `ollamacmf` where you would run Ollama: on a personal machine or a server you control, not on hosts shared with untrusted users, and never expose the Cortiq port. Clients should only use the `ollamacmf` server address.

Models are untrusted input. Only run models from sources you trust.
