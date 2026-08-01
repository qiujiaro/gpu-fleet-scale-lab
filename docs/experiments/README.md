# Experiment Catalog

This directory is the canonical index for experiment names, scope, and status. Historical
day logs describe the order in which the work happened; this catalog defines how the
finished repository names that work.

| ID | Canonical name | Scope | Status |
| --- | --- | --- | --- |
| Exp0 | Load Generator and Client Calibration | Rate-controlled Pod submission, retry/recording behavior, and the 50 Pod/s client preflight | Complete |
| Exp1 | Fleet Readiness Scaling | Time for simulated GPU nodes to become Ready while scaling the KWOK fleet from 1,000 to 2,000 nodes | Complete |
| Exp2 | TopoGang Scheduler, Behavior, and Load Profiling | Two-scheduler isolation, insufficient-capacity behavior, and the 4–64 Pod/s controlled-load matrix | Complete within the tested range |
| Exp3 | Burst and API Server Pressure | Burst scale-out and APF pressure study | Planned follow-up |

## Naming rules

- `Exp1` refers only to fleet readiness.
- `Exp0` covers the load generator and its client-capacity calibration.
- `Exp2` contains three phases: `two-scheduler-isolation`, `behavior-preview`, and
  `controlled-load`. The earlier
  working name `Exp2 controlled-load phase` referred to the profiling phase and is retired.
- Existing run IDs beginning with `exp2p-` are retained inside recorded artifacts because
  a run ID is provenance, not a current experiment name.
- A status of **Complete** means the stated question was run and reported for the declared
  setup. It does not claim production-GPU validity or saturation outside the tested range.

## Data layout

```text
experiments/
  exp0-loadgen/               Exp0 calibration artifacts
  exp1-fleet-readiness/       Exp1 committed results
  exp2-topogang/
    two-scheduler-isolation/  Exp2 scheduler ownership check from Day 4
    behavior-preview/         Exp2 insufficient-capacity comparison
    controlled-load/
      published/              formal result batch
      smoke/                  successful reference smoke
      local/                  default ignored output for new runs
```
