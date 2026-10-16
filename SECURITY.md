# Security policy for ollamacmf

`ollamacmf` is an independent experimental fork of
[Ollama](https://github.com/ollama/ollama). This policy covers changes in this
repository, including the CMF/Cortiq sidecar adapter, runtime-lock verification,
and related CI workflows.

## Reporting a vulnerability

Please **do not** open a public issue for a suspected vulnerability in this
fork. Use the repository's private
[GitHub Security Advisories form](https://github.com/infosave2007/ollamacmf/security/advisories/new)
instead. Include:

- affected fork revision and OS/architecture;
- whether a CMF model, Cortiq runtime, or workflow input is involved;
- a minimal reproducible case and expected/actual behavior;
- impact assessment; and
- any suggested mitigation.

Do not send fork-specific reports to Ollama, Inc. or its upstream contact
channels. For a vulnerability that affects unmodified upstream Ollama rather
than this fork, also follow the
[upstream security policy](https://github.com/ollama/ollama/blob/main/SECURITY.md).

## Current CMF deployment limitation

The CMF bootstrap starts Cortiq with unauthenticated HTTP on an ephemeral
loopback port. Loopback is not an authorization boundary, and the current
reserve-then-bind launch sequence has a local time-of-check/time-of-use race.

Use it only on a trusted, single-user host. Do not expose or reverse-proxy the
sidecar port, run it alongside untrusted local workloads, or rely on it to
protect prompts, model data, tools, or credentials. These are known deployment
limitations, not a claim that the integration is suitable for production.

See the [CMF/Cortiq security boundary](./docs/cmf.md#security-boundary) for the
operator guidance and [CMF engine contract](./docs/cmf-engine.md) for the
technical boundary.

## Third-party runtime inputs

Cortiq runtime release assets are selected through the checked-in
[`cmf.lock.json`](./cmf.lock.json). The lock records transfer-integrity checks
against release-provided checksums; it is not a cryptographic provenance
attestation. Do not replace a locked runtime with `latest`, a different target,
or an unreviewed source build. Report a suspected lock/asset verification issue
privately with the affected lock revision and artifact details.
