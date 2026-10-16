---
title: CMF runtime intake automation
sidebarTitle: Runtime intake
---

The fork has an automated **candidate intake** workflow for new
[CMF/Cortiq](https://github.com/infosave2007/cmf) releases. It is intentionally
not an automatic product release pipeline.

> [!IMPORTANT]
> A successful intake run can create a reviewable `cmf.lock.json` candidate
> PR. It does **not** build an `ollamacmf` installer, embed Cortiq, publish a
> container, sign/notarize a binary, create a GitHub release, or update a
> `latest` tag.

This boundary is deliberate: an upstream release event is untrusted input and
the current fork has no reviewed bundled-runtime distribution format.

## Current trigger and outcome

Workflow: [`.github/workflows/cmf-release-sync.yml`](https://github.com/infosave2007/ollamacmf/blob/main/.github/workflows/cmf-release-sync.yml)

| Trigger | What it does |
| --- | --- |
| Schedule: `17 */4 * * *` | Reconciles the latest upstream release at minute 17 every four hours |
| Repository dispatch: `cmf-release` | Wakes the same intake process; it does not trust event payload release data |
| Candidate PR | Creates a unique, non-force candidate branch named from the tag and source commit, changing only `cmf.lock.json` |
| PR verification | Rechecks locked inputs and runs the tiny fixture through the Linux `amd64` Cortiq archive and this fork's Go adapter |

There is no `workflow_dispatch` trigger on the write-capable intake workflow.
That prevents a UI-selected ref from changing the authority that supplies the
trusted default-branch workflow code. A maintainer with repository dispatch
permission can request reconciliation explicitly, for example:

```sh
gh api --method POST repos/infosave2007/ollamacmf/dispatches \
  -f event_type=cmf-release
```

The schedule is the normal automated path because GitHub cannot directly
subscribe one repository's workflow to another repository's release event.

## What the intake verifier accepts

The workflow independently reads upstream GitHub Release and annotated-tag APIs
and requires all of the following before it proposes a lock update:

1. A non-draft, non-prerelease `vMAJOR.MINOR.PATCH` release with an annotated
   tag that resolves directly to a commit.
2. One reviewed archive and adjacent `.sha256` asset for every current desktop
   target in the lock scope: Linux `amd64`, macOS `amd64`/`arm64`, and Windows
   `amd64`/`arm64`.
3. Bounded sizes, matching release URLs/asset metadata, valid checksums, and a
   strict one-executable archive layout for each target.
4. No unreviewed expansion of the platform set. For example, a newly appearing
   Linux `arm64` archive fails until a separate lock/schema/docs review adds it.
5. A tag-and-source-commit candidate branch that does not already exist. The
   write job uses a create-only lease and refuses to overwrite reviewer work.

Intake downloads archives only to validate them; it does not execute the
upstream runtime in its write-capable job. The read-only candidate verification
workflow then processes the bounded candidate data and runs the Linux fixture
under a scrubbed environment with no token passed to Cortiq.

## Review and promotion runbook

A candidate PR is evidence for review, not approval to ship. Before merging a
new lock revision, review at least:

1. The tag, resolved source commit, release metadata, archive/checksum URLs,
   checksums, sizes, and archive entry names in the diff.
2. The candidate workflow results, including the adapter-level Linux E2E.
3. The matching fixture record. A runtime tag change requires a deliberately
   regenerated, hashed, license-reviewed fixture or CI fails instead of silently
   testing mismatched bytes.
4. Licensing, provenance, and the upstream tag's verification state. A release
   checksum establishes transfer integrity; it is not automatically a signing
   or provenance attestation.
5. Manual host testing for every platform intended for a distribution.

Merging a lock candidate only updates source-controlled metadata. It does not
make a public binary safe or available.

## What must exist before auto-publishing is enabled

A future release pipeline should run only after a reviewed lock revision and
must be a separate, protected workflow. At minimum it needs:

- a documented payload layout that installs the exact locked Cortiq executable
  beside the fork binary without replacing any other runner;
- platform build and runtime E2E coverage for each shipped target;
- explicit third-party notices for Cortiq (Apache-2.0) and reproducible
  provenance linking the fork revision, lock revision, runtime archive, and
  emitted artifact;
- signing/notarization and retained checksums/SBOMs for distributed artifacts;
- protected branch/release permissions, required checks, and a human promotion
  decision; and
- no fallback to `latest`, another architecture, or a source build that is not
  represented by the lock.

Until those controls exist, the honest release state is **source-only,
candidate-intake automation only**.

## Relationship to the lock and tests

[`cmf.lock.json`](https://github.com/infosave2007/ollamacmf/blob/main/cmf.lock.json) is the canonical record of reviewed
runtime inputs and the matching tiny E2E fixture. The detailed
[CMF engine contract](cmf-engine.md) explains package-layout and trust
invariants. The [CMF/Cortiq guide](cmf.md) explains how an operator manually
uses a verified locked runtime today.
