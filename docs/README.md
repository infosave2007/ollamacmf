# ollamacmf documentation source

These are the documentation sources for the `ollamacmf` fork. Start with the
repository [README](https://github.com/infosave2007/ollamacmf) or the
[CMF/Cortiq guide](./cmf.md); they are the authoritative entry points for this
fork's local CMF integration.

## Fork-specific documentation

- [CMF/Cortiq guide](./cmf.md) — source build, verified runtime selection,
  local import, API use, compatibility, security, and troubleshooting.
- [CMF engine contract](./cmf-engine.md) — maintainer-facing sidecar, model,
  runtime-lock, package-layout, and trust invariants.
- [CMF runtime intake automation](./cmf-release-automation.md) — scheduled and
  repository-dispatch candidate intake, review gate, and the deliberately
  absent auto-publishing step.
- [Development guide](./development.md) — full native build prerequisites and
  CI for the fork.

The docs navigation is defined in [`docs.json`](./docs.json). The
[`.mintignore`](./.mintignore) policy publishes only the CMF landing, guide,
maintainer, and clearly marked base-development pages. Inherited Ollama
material is retained in Git for source history but is deliberately excluded
from the public `ollamacmf` documentation site. Do not link to it as fork
documentation.

For canonical upstream documentation, use [docs.ollama.com](https://docs.ollama.com).
For the fork's source, issues, and releases, use
[github.com/infosave2007/ollamacmf](https://github.com/infosave2007/ollamacmf).
