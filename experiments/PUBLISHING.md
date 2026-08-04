# Experiment Data Publication Policy

Raw local runs are ignored by default. Do not choose a run because it has the
best-looking latency; that would bias the reported result.

Publish one **complete representative batch** only after it passes the experiment
validity rules:

- all required cells and three repeats are present;
- fixed controls and paired seeds match the Catalog;
- submission failures, rate limiting, censoring, and missing telemetry are disclosed;
- no run is removed merely because its result is unfavorable; and
- the batch ID, Git SHA, dirty-worktree state, and environment remain in `meta.json`.

Place the selected batch under:

```text
experiments/
  exp1-scale-sweep/published/<batch-id>/
  exp2-gang/published/<batch-id>/
  exp3-burst/published/<batch-id>/
```

Small deterministic smoke fixtures may remain under a `reference/` or existing
`smoke/reference-*` directory. Large local, failed, exploratory, and profiling
runs stay outside Git; summarize important failures in documentation instead of
committing every raw artifact.

Before committing, verify exactly what will be uploaded:

```bash
git status --short
git check-ignore -v <path-to-a-local-run>
git check-ignore -v <path-under-published>
```

For a path under `published/`, `git check-ignore` should produce no output.
