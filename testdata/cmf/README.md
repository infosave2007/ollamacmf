# CMF integration fixture

`tiny-v0.8.19.cmf` is a **real, CPU-runnable generative CMF v2 model** used
only by the unprivileged Cortiq runtime E2E gate. It is not the synthetic
128-byte envelope used by Go unit tests.

Its exact source commit, generator-script hashes, generation commands,
toolchain record, artifact checksum, license, and update policy are in
[`tiny-v0.8.19.provenance.json`](tiny-v0.8.19.provenance.json). The CI workflow
checks the file against the canonical lock before giving the downloaded runner
an execution environment with no secrets or write credentials.

Do not replace this fixture just because CMF publishes a new release. A lock
update must include a deliberate, reviewed regeneration and compatibility
validation for the matching release tag.
