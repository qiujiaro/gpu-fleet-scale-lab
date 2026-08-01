# Day 07 — Exp1 Fleet Readiness + Exp2 TopoGang

- **Status:** Completed; canonical results are summarized in `README.md` and `docs/REPORT.md`.
- **Planned deliverable:** Exp1 fleet-readiness data plus Exp2 behavior and load results.
- **Core module to hand-write:** Experiment orchestration.

## Goals for today
- [x] Run Exp1 Fleet Readiness Scaling through 2,000 simulated nodes.
- [x] Run the Exp2 insufficient-capacity behavior comparison.
- [x] Run the Exp2 4–64 Pod/s controlled-load matrix with three attempts per level.

## What I did

- Exp1 reached 2,000 Ready simulated GPU nodes; the final +500 step took 3 seconds.
- Exp2 reproduced 3/4 partial placement with the default scheduler and 0/4 with TopoGang.
- Exp2 produced 14 valid controlled-load runs; all valid runs completed 800/800 Pods.

## What I learned
-

## Key results / numbers

- Exp1 source: `experiments/exp1-fleet-readiness/results.csv`.
- Exp2 source: `experiments/exp2-topogang/`.
- No saturation knee was observed through 64 Pod/s; this is a bounded result, not a
  production-throughput claim.

## Blockers & how I solved them
-

## Open questions
-

## Commit(s) / artifacts
-

## Plan for tomorrow
-
