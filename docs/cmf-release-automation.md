---
title: Runtime updates
sidebarTitle: Runtime updates
---

`ollamacmf` pins one Cortiq release in [`cmfrunner/cmf.lock.json`](https://github.com/infosave2007/ollamacmf/blob/main/cmfrunner/cmf.lock.json). This page explains how that pin moves to a newer Cortiq release and what happens afterwards.

## Automatic candidate pull requests

[`cmf-release-sync.yml`](https://github.com/infosave2007/ollamacmf/blob/main/.github/workflows/cmf-release-sync.yml) checks the latest stable release of [infosave2007/cmf](https://github.com/infosave2007/cmf) every four hours (and on a `cmf-release` repository dispatch). When a new release has a complete set of desktop archives with matching checksums, the workflow opens a pull request that changes only `cmfrunner/cmf.lock.json`.

[`cmf-runner-smoke.yml`](https://github.com/infosave2007/ollamacmf/blob/main/.github/workflows/cmf-runner-smoke.yml) runs on that pull request: it re-verifies every archive against the lock and runs the Linux build of the new runtime against the test fixture through the Go adapter.

To trigger a check immediately:

```sh
gh api --method POST repos/infosave2007/ollamacmf/dispatches -f event_type=cmf-release
```

## Reviewing a candidate

Before merging, check that:

1. The tag and source commit in the diff are the release you expect.
2. The smoke workflow is green. A red run usually means Cortiq changed its HTTP API or the fixture needs regenerating.
3. The fixture record still matches. The tiny fixture is tied to a Cortiq version; a format change requires a regenerated fixture and an updated `e2eFixture` block.
4. `go test ./cmfrunner ./server` passes locally with the new lock, and a real model runs end to end on at least one platform.

Merging updates what new builds of `ollamacmf` download and bundle. Existing installations keep their current runtime until they are updated; `ollamacmf cmf status` shows when the installed runtime is older than the pinned one.

## Publishing a release

Tag the merged commit and push the tag. The [release workflow](cmf-engine.md#releases) builds the binaries with the new runtime bundled and publishes them.

```sh
git tag cmf-v0.2.0
git push origin cmf-v0.2.0
```

## Manual lock updates

If the automation is unavailable, edit the lock by hand: for each platform fill in the archive name, URL, size in bytes, SHA-256 and the executable name from the GitHub release assets, and update the `upstream.release` and `upstream.source` blocks. Then run the smoke test:

```sh
go test ./cmfrunner ./server
```

and open a pull request with the change.
