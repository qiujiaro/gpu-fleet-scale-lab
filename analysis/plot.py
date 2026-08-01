#!/usr/bin/env python3
"""Generate every figure in the report from CSV under experiments/.

No number in analysis/figures/ is typed by hand: each figure is discovered from run
files on disk, and a figure whose input data does not exist yet is SKIPPED with a
printed reason rather than drawn from placeholder values.

    python3 analysis/plot.py                          # all figures it can build
    python3 analysis/plot.py --experiments experiments --out analysis/figures
    python3 analysis/plot.py --in experiments/diagnostics/local/demo.csv --out analysis/figures
    python3 analysis/plot.py --slo-ms 5000            # adds an SLO reference line

Input layout (one "run" = one basename; every file is optional except the summary):

    experiments/diagnostics/rejected-pod-latency-baseline/N500-run1.csv            per-pod timelines (profiler)
    experiments/diagnostics/rejected-pod-latency-baseline/N500-run1-summary.csv    per-phase quantiles (profiler)
    experiments/diagnostics/rejected-pod-latency-baseline/N500-run1-meta.json      control variables for the run
    experiments/diagnostics/rejected-pod-latency-baseline/N500-run1-host.csv       host load sampler
    experiments/exp2-topogang/topogang-run1-groups.csv        per-PodGroup outcomes
    experiments/exp3-burst-pressure/opt-on-run1-apiserver.csv  apiserver P99 time series
    experiments/exp3-burst-pressure/opt-on-run1-pressure.csv   scalar pressure counters

CSV schemas (produced by cmd/profiler and the run-exp* scripts):

    *-summary.csv   phase,count,total,censored,censored_rate,p50_ms,p95_ms,p99_ms
    *.csv           uid,submit_ts,scheduled_ts,bound_ts,ready_ts,
                    scheduling_ms,binding_ms,coldstart_ms,e2e_ms,censored
    *-host.csv      ts,cpu_percent,mem_mb
    *-groups.csv    group,min_member,scheduled_members,ready,time_to_ready_ms,rejected
    *-apiserver.csv ts,p99_ms
    *-pressure.csv  metric,value            (e.g. http_429_total, apf_inqueue_peak)

    *-meta.json     {"nodes": 500, "run": 1, "scheduler": "default",
                     "optimize": false, "qps": 50, "seed": 42, ...}

meta.json is the source of truth for control variables. Without it the run's nodes /
scheduler / optimize values are parsed from the filename as a fallback, and a warning
is printed — the fallback exists so an interrupted experiment still plots, not as the
intended path.
"""

from __future__ import annotations

import argparse
import csv
import json
import re
import sys
from collections import defaultdict
from datetime import datetime
from pathlib import Path

try:
    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
except ImportError:  # pragma: no cover - environment problem, not a data problem
    sys.exit("matplotlib is required: pip install -r analysis/requirements.txt")

PHASES = ("scheduling", "binding", "cold-start", "end-to-end")
STACK_PHASES = ("scheduling", "binding", "cold-start")


# --------------------------------------------------------------------------- loading


class Run:
    """One experiment run: its control variables plus whichever CSVs exist."""

    def __init__(self, base: Path, meta: dict, meta_source: str):
        self.base = base
        self.meta = meta
        self.meta_source = meta_source

    @property
    def name(self) -> str:
        return self.base.name

    def path(self, suffix: str) -> Path | None:
        p = self.base.with_name(self.base.name + suffix)
        return p if p.exists() else None

    def get(self, key, default=None):
        return self.meta.get(key, default)

    def summary(self) -> dict[str, dict[str, float]]:
        """phase -> {count, total, censored, censored_rate, p50_ms, p95_ms, p99_ms}."""
        path = self.path("-summary.csv")
        if path is None:
            return {}
        out = {}
        for row in _rows(path):
            phase = row.get("phase", "").strip()
            if not phase:
                continue
            out[phase] = {k: _num(v) for k, v in row.items() if k != "phase"}
        return out


def discover(exp_dir: Path) -> list[Run]:
    """Find every run under exp_dir, keyed by its -summary.csv."""
    runs = []
    for summary in sorted(exp_dir.rglob("*-summary.csv")):
        base = summary.with_name(summary.name[: -len("-summary.csv")])
        meta_path = base.with_name(base.name + "-meta.json")
        if meta_path.exists():
            meta, source = json.loads(meta_path.read_text()), "meta.json"
        else:
            meta, source = _meta_from_filename(base.name), "filename"
            warn(f"{summary.relative_to(exp_dir.parent)}: no meta.json, "
                 f"guessed {meta} from the filename")
        runs.append(Run(base, meta, source))
    return runs


def _meta_from_filename(name: str) -> dict:
    """Last-resort control variables: N<nodes>, run<k>, opt-on/off, scheduler name."""
    meta: dict = {}
    if m := re.search(r"[Nn](\d+)", name):
        meta["nodes"] = int(m.group(1))
    if m := re.search(r"run[-_]?(\d+)", name):
        meta["run"] = int(m.group(1))
    if m := re.search(r"opt(?:imize)?[-_]?(on|off|true|false)", name):
        meta["optimize"] = m.group(1) in ("on", "true")
    for sched in ("topogang", "default"):
        if sched in name:
            meta["scheduler"] = sched
            break
    return meta


def _rows(path: Path) -> list[dict]:
    with path.open(newline="") as f:
        return list(csv.DictReader(f))


def _num(value):
    """CSV cell -> float, or None for an empty/unparsable cell (never 0)."""
    if value is None:
        return None
    value = value.strip()
    if value == "":
        return None
    if value.lower() in ("true", "false"):
        return value.lower() == "true"
    try:
        return float(value)
    except ValueError:
        return None


def _ts(value):
    """RFC3339(Nano) timestamp -> epoch seconds, or None."""
    if not value:
        return None
    text = value.strip().replace("Z", "+00:00")
    if "." in text:  # datetime rejects >6 fractional digits
        head, _, tail = text.partition(".")
        digits = re.match(r"(\d+)(.*)", tail)
        if digits:
            text = f"{head}.{digits.group(1)[:6]}{digits.group(2)}"
    try:
        return datetime.fromisoformat(text).timestamp()
    except ValueError:
        return None


# ------------------------------------------------------------------------ statistics


def group_by(runs: list[Run], key: str) -> dict:
    """Group runs by a control variable, dropping those that do not declare it."""
    groups: dict = defaultdict(list)
    for run in runs:
        value = run.get(key)
        if value is None:
            warn(f"{run.name}: no {key!r} in {run.meta_source}, excluded from this figure")
            continue
        groups[value].append(run)
    return dict(sorted(groups.items(), key=lambda kv: _sort_key(kv[0])))


def _sort_key(value):
    return (0, value, "") if isinstance(value, (int, float)) else (1, 0, str(value))


def mean(values: list[float]) -> float:
    return sum(values) / len(values)


def whiskers(values: list[float]) -> tuple[float, float]:
    """Asymmetric min/max error bars around the mean — the observed spread, not a model."""
    m = mean(values)
    return m - min(values), max(values) - m


def errorbars(series: list[list[float]]) -> tuple[list[float], list[list[float]]]:
    means = [mean(v) for v in series]
    lo, hi = zip(*(whiskers(v) for v in series))
    return means, [list(lo), list(hi)]


# ----------------------------------------------------------------------------- output


SKIPPED: list[str] = []
WARNED: list[str] = []


def warn(message: str) -> None:
    WARNED.append(message)
    print(f"  warning: {message}")


def skip(figure: str, reason: str) -> None:
    SKIPPED.append(f"{figure}: {reason}")
    print(f"  skip {figure} — {reason}")


def save(fig, out_dir: Path, name: str) -> None:
    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / f"{name}.png"
    fig.savefig(path, dpi=150, bbox_inches="tight")
    plt.close(fig)
    print(f"  wrote {path}")


def annotate_source(ax, runs_used: int, runs_per_point: str) -> None:
    ax.text(
        0.0, -0.18,
        f"generated by analysis/plot.py from experiments/ · {runs_used} runs ({runs_per_point})",
        transform=ax.transAxes, fontsize=7, color="0.4", va="top",
    )


# ---------------------------------------------------------------------------- figures


def fig1_scheduling_p99_vs_nodes(runs, out_dir, slo_ms):
    """Fig 1 — node count vs scheduling P99, error bars over repeats, optional SLO line."""
    groups = group_by(runs, "nodes")
    xs, series = [], []
    for nodes, group in groups.items():
        values = [s["scheduling"]["p99_ms"] for r in group
                  if (s := r.summary()).get("scheduling", {}).get("p99_ms") is not None]
        if values:
            xs.append(nodes)
            series.append(values)
    if not xs:
        return skip("exp1-scheduling-p99-vs-nodes", "no exp1 summary CSV with a scheduling p99")

    means, yerr = errorbars(series)
    fig, ax = plt.subplots(figsize=(7, 4.5))
    ax.errorbar(xs, means, yerr=yerr, marker="o", capsize=4, label="scheduling P99")
    if slo_ms is not None:
        ax.axhline(slo_ms, ls="--", color="crimson", lw=1,
                   label=f"SLO reference {slo_ms:g} ms (--slo-ms)")
    ax.set_xlabel("simulated GPU nodes (kwok)")
    ax.set_ylabel("scheduling latency P99 (ms)")
    ax.set_title("Exp1 — scheduling P99 vs fleet size (simulated control plane)")
    ax.grid(alpha=0.3)
    ax.legend()
    annotate_source(ax, sum(len(v) for v in series), "mean, whiskers = min/max")
    save(fig, out_dir, "exp1-scheduling-p99-vs-nodes")


def fig2_throughput_vs_nodes(runs, out_dir):
    """Fig 2 — node count vs scheduling throughput, measured from per-pod timelines."""
    groups = group_by(runs, "nodes")
    xs, series = [], []
    for nodes, group in groups.items():
        values = [t for r in group if (t := _throughput(r)) is not None]
        if values:
            xs.append(nodes)
            series.append(values)
    if not xs:
        return skip("exp1-throughput-vs-nodes",
                    "no per-pod timeline CSV with submit_ts/scheduled_ts")

    means, yerr = errorbars(series)
    fig, ax = plt.subplots(figsize=(7, 4.5))
    ax.errorbar(xs, means, yerr=yerr, marker="s", color="tab:green", capsize=4)
    ax.set_xlabel("simulated GPU nodes (kwok)")
    ax.set_ylabel("scheduling throughput (pods/s)")
    ax.set_title("Exp1 — scheduled pods/s vs fleet size")
    ax.grid(alpha=0.3)
    annotate_source(ax, sum(len(v) for v in series), "mean, whiskers = min/max")
    save(fig, out_dir, "exp1-throughput-vs-nodes")


def _throughput(run: Run) -> float | None:
    """Scheduled pods divided by the wall-clock span from first submit to last schedule."""
    path = run.path(".csv")
    if path is None:
        return None
    submits, scheduled = [], []
    for row in _rows(path):
        if (t := _ts(row.get("submit_ts"))) is not None:
            submits.append(t)
        if (t := _ts(row.get("scheduled_ts"))) is not None:
            scheduled.append(t)
    if not submits or not scheduled:
        return None
    span = max(scheduled) - min(submits)
    if span <= 0:
        warn(f"{run.name}: non-positive scheduling window ({span:.3f}s), throughput skipped")
        return None
    return len(scheduled) / span


def fig3_host_load(runs, out_dir):
    """Fig 3 — peak host CPU/memory per fleet size, to mark simulation-host saturation."""
    groups = group_by(runs, "nodes")
    xs, cpu, mem = [], [], []
    for nodes, group in groups.items():
        peaks = [p for r in group if (p := _host_peak(r)) is not None]
        if peaks:
            xs.append(nodes)
            cpu.append(mean([p[0] for p in peaks]))
            mem.append(mean([p[1] for p in peaks]) / 1024.0)
    if not xs:
        return skip("exp1-host-load", "no *-host.csv sampled alongside the runs")

    fig, ax = plt.subplots(figsize=(7, 4.5))
    ax.plot(xs, cpu, marker="o", color="tab:red", label="peak host CPU")
    ax.set_xlabel("simulated GPU nodes (kwok)")
    ax.set_ylabel("peak host CPU (%)", color="tab:red")
    ax.grid(alpha=0.3)
    ax2 = ax.twinx()
    ax2.plot(xs, mem, marker="^", color="tab:blue", label="peak host memory")
    ax2.set_ylabel("peak host memory (GiB)", color="tab:blue")
    ax.set_title("Exp1 — simulation host load (confounder: host saturation ≠ control-plane limit)")
    annotate_source(ax, sum(len(g) for g in groups.values()), "mean of per-run peaks")
    save(fig, out_dir, "exp1-host-load")


def _host_peak(run: Run) -> tuple[float, float] | None:
    path = run.path("-host.csv")
    if path is None:
        return None
    cpu = [v for row in _rows(path) if (v := _num(row.get("cpu_percent"))) is not None]
    mem = [v for row in _rows(path) if (v := _num(row.get("mem_mb"))) is not None]
    if not cpu or not mem:
        return None
    return max(cpu), max(mem)


def fig4_podgroup_ready_rate(runs, out_dir):
    """Fig 4 — all-ready PodGroup rate and half-placed pods, default vs topogang."""
    groups = group_by(runs, "scheduler")
    labels, ready, partial = [], [], []
    for scheduler, group in groups.items():
        stats = [s for r in group if (s := _group_stats(r)) is not None]
        if not stats:
            continue
        labels.append(scheduler)
        ready.append([s["ready_rate"] * 100 for s in stats])
        partial.append([s["partial_pods"] for s in stats])
    if not labels:
        return skip("exp2-podgroup-ready-rate", "no *-groups.csv alongside the gang runs")

    ready_m, ready_e = errorbars(ready)
    partial_m, partial_e = errorbars(partial)
    x = range(len(labels))
    fig, (ax, ax2) = plt.subplots(1, 2, figsize=(9, 4.5))
    ax.bar(x, ready_m, yerr=ready_e, capsize=4, color="tab:blue")
    ax.set_xticks(list(x), labels)
    ax.set_ylabel("PodGroups fully ready (%)")
    ax.set_ylim(0, 100)
    ax2.bar(x, partial_m, yerr=partial_e, capsize=4, color="tab:orange")
    ax2.set_xticks(list(x), labels)
    ax2.set_ylabel("pods holding resources in a never-ready group")
    fig.suptitle("Exp2 — gang semantics under contention")
    annotate_source(ax, sum(len(v) for v in ready), "mean, whiskers = min/max")
    save(fig, out_dir, "exp2-podgroup-ready-rate")


def _group_stats(run: Run) -> dict | None:
    path = run.path("-groups.csv")
    if path is None:
        return None
    total = ready = partial = 0
    for row in _rows(path):
        total += 1
        is_ready = _num(row.get("ready")) is True or str(row.get("ready")).lower() == "true"
        if is_ready:
            ready += 1
        else:
            partial += int(_num(row.get("scheduled_members")) or 0)
    if total == 0:
        return None
    return {"ready_rate": ready / total, "partial_pods": partial}


def fig5_time_to_ready_cdf(runs, out_dir):
    """Fig 5 — PodGroup time-to-ready CDF, pooled across repeats per scheduler."""
    groups = group_by(runs, "scheduler")
    fig, ax = plt.subplots(figsize=(7, 4.5))
    plotted = 0
    for scheduler, group in groups.items():
        values = []
        for run in group:
            if (path := run.path("-groups.csv")) is None:
                continue
            values += [v for row in _rows(path)
                       if (v := _num(row.get("time_to_ready_ms"))) is not None]
        if not values:
            continue
        values.sort()
        ys = [(i + 1) / len(values) for i in range(len(values))]
        ax.step(values, ys, where="post", label=f"{scheduler} (n={len(values)})")
        plotted += 1
    if plotted == 0:
        plt.close(fig)
        return skip("exp2-time-to-ready-cdf", "no time_to_ready_ms samples in any *-groups.csv")

    ax.set_xlabel("PodGroup time-to-ready (ms)")
    ax.set_ylabel("fraction of PodGroups")
    ax.set_ylim(0, 1)
    ax.set_title("Exp2 — PodGroup time-to-ready CDF")
    ax.grid(alpha=0.3)
    ax.legend()
    annotate_source(ax, sum(len(g) for g in groups.values()), "pooled over repeats")
    save(fig, out_dir, "exp2-time-to-ready-cdf")


def fig6_apiserver_p99_timeseries(runs, out_dir):
    """Fig 6 — apiserver P99 over time during the burst, optimize on vs off."""
    groups = group_by(runs, "optimize")
    fig, ax = plt.subplots(figsize=(7.5, 4.5))
    colors = {True: "tab:green", False: "tab:red"}
    plotted = 0
    for optimize, group in groups.items():
        label = f"optimize={'on' if optimize else 'off'}"
        for i, run in enumerate(group):
            if (path := run.path("-apiserver.csv")) is None:
                continue
            points = [(t, v) for row in _rows(path)
                      if (t := _ts(row.get("ts")) or _num(row.get("ts"))) is not None
                      and (v := _num(row.get("p99_ms"))) is not None]
            if not points:
                continue
            points.sort()
            t0 = points[0][0]
            ax.plot([t - t0 for t, _ in points], [v for _, v in points],
                    color=colors.get(bool(optimize), "tab:gray"), alpha=0.75, lw=1.2,
                    label=label if i == 0 else None)
            plotted += 1
    if plotted == 0:
        plt.close(fig)
        return skip("exp3-apiserver-p99-timeseries", "no *-apiserver.csv alongside the burst runs")

    ax.set_xlabel("seconds since run start")
    ax.set_ylabel("apiserver request duration P99 (ms)")
    ax.set_title("Exp3 — apiserver P99 during burst scale-out")
    ax.grid(alpha=0.3)
    ax.legend()
    annotate_source(ax, plotted, "one line per run")
    save(fig, out_dir, "exp3-apiserver-p99-timeseries")


def fig7_pressure_bars(runs, out_dir):
    """Fig 7 — 429s, APF queue peak and end-to-end P99, optimize on vs off."""
    metrics = ["http_429_total", "apf_inqueue_peak", "e2e_p99_ms"]
    collected: dict = defaultdict(lambda: defaultdict(list))
    for run in runs:
        optimize = run.get("optimize")
        if optimize is None:
            continue
        label = f"optimize={'on' if optimize else 'off'}"
        if path := run.path("-pressure.csv"):
            for row in _rows(path):
                metric = (row.get("metric") or "").strip()
                if metric in metrics and (v := _num(row.get("value"))) is not None:
                    collected[metric][label].append(v)
        if (v := run.summary().get("end-to-end", {}).get("p99_ms")) is not None:
            collected["e2e_p99_ms"][label].append(v)

    present = [m for m in metrics if collected[m]]
    if not present:
        return skip("exp3-pressure-bars",
                    "no *-pressure.csv counters and no end-to-end p99 in the burst runs")

    labels = sorted({label for m in present for label in collected[m]})
    fig, axes = plt.subplots(1, len(present), figsize=(3.2 * len(present), 4.2))
    axes = axes if len(present) > 1 else [axes]
    for ax, metric in zip(axes, present):
        series = [collected[metric].get(label, []) for label in labels]
        usable = [(label, v) for label, v in zip(labels, series) if v]
        means, err = errorbars([v for _, v in usable])
        ax.bar(range(len(usable)), means, yerr=err, capsize=4,
               color=["tab:green" if "on" in label else "tab:red" for label, _ in usable])
        ax.set_xticks(range(len(usable)), [label for label, _ in usable], fontsize=8)
        ax.set_title(metric, fontsize=10)
        ax.grid(alpha=0.3, axis="y")
    fig.suptitle("Exp3 — API-server pressure and end-to-end cost, optimize off vs on")
    annotate_source(axes[0], len(runs), "mean, whiskers = min/max")
    save(fig, out_dir, "exp3-pressure-bars")


def fig8_latency_breakdown(runs, out_dir, quantile="p50_ms"):
    """Fig 8 — stacked scheduling/binding/cold-start split per fleet size.

    Stacks the median, not the P99: quantiles are not additive, so a stacked P99 chart
    would show a total no pod ever experienced.
    """
    groups = group_by(runs, "nodes")
    xs, stacks, counts = [], [], []
    for nodes, group in groups.items():
        per_phase = {}
        for phase in STACK_PHASES:
            values = [v for r in group
                      if (v := r.summary().get(phase, {}).get(quantile)) is not None]
            if values:
                per_phase[phase] = mean(values)
        if per_phase:
            xs.append(str(nodes))
            stacks.append(per_phase)
            counts.append(len(group))
    if not xs:
        return skip("latency-breakdown-stacked", f"no summary CSV with a {quantile} per phase")

    fig, ax = plt.subplots(figsize=(7, 4.5))
    bottom = [0.0] * len(xs)
    for phase in STACK_PHASES:
        heights = [s.get(phase, 0.0) for s in stacks]
        ax.bar(xs, heights, bottom=bottom, label=phase)
        bottom = [b + h for b, h in zip(bottom, heights)]
    ax.set_xlabel("simulated GPU nodes (kwok)")
    ax.set_ylabel(f"latency {quantile.replace('_ms', '')} (ms)")
    ax.set_title("End-to-end latency breakdown (median per phase; quantiles are not additive)")
    ax.legend()
    ax.grid(alpha=0.3, axis="y")
    annotate_source(ax, sum(counts), f"mean of per-run {quantile}")
    save(fig, out_dir, "latency-breakdown-stacked")


# -------------------------------------------------------------------------------- cli


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--experiments", type=Path, default=Path("experiments"),
                        help="experiments root containing exp1-*/exp2-*/exp3-* (default: experiments)")
    parser.add_argument("--in", dest="single", type=Path,
                        help="single run CSV (or its directory) to plot instead of the full tree")
    parser.add_argument("--out", type=Path, default=Path("analysis/figures"),
                        help="figure output directory (default: analysis/figures)")
    parser.add_argument("--slo-ms", type=float,
                        help="draw a horizontal SLO reference line at this latency")
    parser.add_argument("--stack-quantile", default="p50_ms",
                        choices=["p50_ms", "p95_ms", "p99_ms"],
                        help="quantile stacked in the breakdown figure (default: p50_ms)")
    args = parser.parse_args(argv)

    if args.single is not None:
        target = args.single if args.single.is_dir() else args.single.parent
        exp1 = exp2 = exp3 = discover(target)
        print(f"single-directory mode: {target}")
    else:
        exp1 = discover(args.experiments / "exp1-fleet-readiness")
        exp2 = discover(args.experiments / "exp2-topogang")
        exp3 = discover(args.experiments / "exp3-burst-pressure")

    print(f"runs found: exp1={len(exp1)} exp2={len(exp2)} exp3={len(exp3)}")
    if not (exp1 or exp2 or exp3):
        print("no run data yet — run the experiments first (scripts/run-exp*.sh); "
              "figures are only ever generated from CSV, never from placeholder numbers")
        return 1

    fig1_scheduling_p99_vs_nodes(exp1, args.out, args.slo_ms)
    fig2_throughput_vs_nodes(exp1, args.out)
    fig3_host_load(exp1, args.out)
    fig4_podgroup_ready_rate(exp2, args.out)
    fig5_time_to_ready_cdf(exp2, args.out)
    fig6_apiserver_p99_timeseries(exp3, args.out)
    fig7_pressure_bars(exp3, args.out)
    fig8_latency_breakdown(exp1, args.out, args.stack_quantile)

    print(f"\n{8 - len(SKIPPED)}/8 figures written to {args.out}"
          f"{f', {len(SKIPPED)} skipped for missing data' if SKIPPED else ''}")
    for reason in SKIPPED:
        print(f"  - {reason}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
