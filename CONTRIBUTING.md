# Contributing to ollamacmf

Thank you for improving this experimental [Ollama](https://github.com/ollama/ollama)
fork. `ollamacmf` adds a narrow local CMF/Cortiq runner while continuing to
track upstream Ollama behavior where possible. It is maintained independently;
please file fork-specific work in this repository, not in the upstream tracker.

## Before proposing a change

- Read the [CMF/Cortiq guide](./docs/cmf.md) for the public behavior and safety
  boundary.
- Read the [CMF engine contract](./docs/cmf-engine.md) before changing model
  import, runner discovery, runtime packaging, or the canonical lock.
- Use the [development guide](./docs/development.md) for native build and test
  prerequisites.
- Discuss a non-trivial feature or architecture change in an
  [ollamacmf issue](https://github.com/infosave2007/ollamacmf/issues) first.
  This is especially important for API surface, runtime distribution,
  scheduler/resource behavior, or a new CMF platform target.

Do not report security vulnerabilities in a public issue; follow
[SECURITY.md](./SECURITY.md).

## CMF-specific contribution rules

The current adapter is intentionally conservative. A change must not silently
turn a rejected CMF request, Modelfile field, runtime lookup path, or platform
into accepted behavior without documenting and testing it.

For CMF runtime or lock changes:

1. Do not use `latest`, a mutable branch, an unpinned source build, or a
   runtime for a different OS/architecture.
2. Update only through the reviewed lock/fixture contract. Read
   [CMF runtime intake automation](./docs/cmf-release-automation.md).
3. Preserve the candidate-only promotion boundary: a lock update is not a
   request to publish an installer or release artifact.
4. Add or update tests for behavior, including the adapter E2E path when a
   runtime or fixture changes.
5. Keep the sidecar's trusted-single-user security limitation intact unless a
   reviewed authenticated/private runner design replaces it.

## Pull requests

Keep pull requests focused and explain:

- the problem and user-visible behavior change;
- why the change belongs in this fork rather than upstream Ollama or CMF;
- API, compatibility, security, and platform implications; and
- commands and results used to verify it.

Please include tests for code changes and documentation for changed public
behavior. Do not claim packaging, platform support, or automatic publishing
that the checked-in implementation does not provide.

Use a concise commit title in the form:

```text
<area>: <short description>
```

For example: `cmfrunner: reject unsupported raw sampling option`.

## Upstream work

If a bug exists in unmodified upstream Ollama code, consider reporting it to
[ollama/ollama](https://github.com/ollama/ollama) as well. Keep fork-specific
changes separated from upstream fixes where practical so rebases remain
reviewable.
