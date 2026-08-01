#!/usr/bin/env python3
"""Analyze an Exp2 controlled-load batch.

Input is the summary.csv written by scripts/exp2-topogang-load-test.sh. When the
per-run groups.csv files are present, post-submit Gang latency is computed
per Gang as:

    t_after_submit = t_group_ready_ms - t_submit_ms

This avoids the statistically invalid shortcut of subtracting two quantiles.

Example:

    python3 analysis/analyze_exp2.py \
      --input experiments/exp2-topogang/controlled-load/published/exp2p-load-.../summary.csv

Outputs:
    aggregate.csv
    validity-vs-qps.png
    latency-vs-qps.png
    gang-after-submit-vs-qps.png
    bottleneck-signals-vs-qps.png
    report.md
"""

from __future__ import annotations

import argparse
import csv
import math
import statistics
import sys
from collections import defaultdict
from pathlib import Path

try:
    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
except ImportError:
    sys.exit("matplotlib is required: pip install -r analysis/requirements.txt")


NUMERIC_COLUMNS = {
    "target_qps",
    "repeat",
    "expected_pods",
    "submitted",
    "observed",
    "censored",
    "censored_rate",
    "scheduling_p50_ms",
    "scheduling_p95_ms",
    "scheduling_p99_ms",
    "e2e_p50_ms",
    "e2e_p95_ms",
    "e2e_p99_ms",
    "t_submit_p50_ms",
    "t_submit_p95_ms",
    "t_submit_p99_ms",
    "t_group_ready_p50_ms",
    "t_group_ready_p95_ms",
    "t_group_ready_p99_ms",
    "prefilter_calls",
    "prefilter_nodes_avg",
    "registry_lock_calls",
    "registry_lock_wait_total_ms",
    "registry_lock_wait_avg_us",
    "permit_allowed",
    "permit_wait_avg_ms",
    "waiting_iterated_avg",
    "gang_rejects",
    "bind_calls",
    "bind_avg_ms",
}


def number(value: str | None) -> float | None:
    if value is None or value.strip() == "":
        return None
    try:
        result = float(value)
    except ValueError:
        return None
    return result if math.isfinite(result) else None


def load_runs(summary_path: Path) -> list[dict]:
    with summary_path.open(newline="") as stream:
        rows = list(csv.DictReader(stream))
    if not rows:
        raise ValueError(f"{summary_path} contains no runs")

    required = {"run_id", "target_qps", "expected_pods", "observed", "censored"}
    missing = required - set(rows[0])
    if missing:
        raise ValueError(f"{summary_path} is missing columns: {sorted(missing)}")

    for row in rows:
        for key in NUMERIC_COLUMNS:
            row[key] = number(row.get(key))
        expected = row.get("expected_pods") or 0
        observed = row.get("observed") or 0
        censored = row.get("censored") or 0
        row["completion_rate"] = (
            max(0.0, observed - censored) / expected if expected > 0 else None
        )
        row["data_complete"] = (
            expected > 0
            and row.get("submitted") == expected
            and observed == expected
            and censored == 0
        )
        add_group_latencies(row, summary_path.parent)
    return rows


def resolve_run_dir(row: dict, batch_dir: Path) -> Path | None:
    raw = (row.get("result_dir") or "").strip()
    candidates = []
    if raw:
        path = Path(raw)
        candidates.extend([path, batch_dir / path, Path.cwd() / path])
    candidates.append(batch_dir / str(row["run_id"]))
    for candidate in candidates:
        if candidate.is_dir():
            return candidate.resolve()
    return None


def add_group_latencies(row: dict, batch_dir: Path) -> None:
    """Attach exact complete-Gang post-submit samples and their quantiles."""
    row["after_submit_samples_ms"] = []
    run_dir = resolve_run_dir(row, batch_dir)
    row["resolved_run_dir"] = str(run_dir) if run_dir else ""
    if run_dir is None:
        return
    groups_path = run_dir / "groups.csv"
    if not groups_path.exists():
        return

    with groups_path.open(newline="") as stream:
        for group in csv.DictReader(stream):
            if (group.get("censored") or "").lower() == "true":
                continue
            ready = number(group.get("t_group_ready_ms"))
            submit = number(group.get("t_submit_ms"))
            if ready is not None and submit is not None and ready >= submit:
                row["after_submit_samples_ms"].append(ready - submit)

    samples = row["after_submit_samples_ms"]
    row["after_submit_p50_ms"] = quantile(samples, 0.50)
    row["after_submit_p95_ms"] = quantile(samples, 0.95)
    row["after_submit_p99_ms"] = quantile(samples, 0.99)


def quantile(values: list[float], q: float) -> float | None:
    """Nearest-rank quantile, matching the Go profiler's simple empirical summary."""
    if not values:
        return None
    ordered = sorted(values)
    rank = max(1, math.ceil(q * len(ordered)))
    return ordered[rank - 1]


def finite_values(rows: list[dict], key: str) -> list[float]:
    return [value for row in rows if (value := row.get(key)) is not None]


def median(rows: list[dict], key: str) -> float | None:
    values = finite_values(rows, key)
    return statistics.median(values) if values else None


def spread(rows: list[dict], key: str) -> tuple[float, float, float] | None:
    values = finite_values(rows, key)
    if not values:
        return None
    center = statistics.median(values)
    return center, center - min(values), max(values) - center


def group_runs(runs: list[dict]) -> dict[float, list[dict]]:
    grouped: dict[float, list[dict]] = defaultdict(list)
    for row in runs:
        qps = row.get("target_qps")
        if qps is not None:
            grouped[qps].append(row)
    return dict(sorted(grouped.items()))


def valid_for_latency(rows: list[dict]) -> list[dict]:
    return [row for row in rows if row["data_complete"]]


def plot_series(ax, grouped, key, label, *, valid_only=True, marker="o"):
    xs, ys, low, high = [], [], [], []
    for qps, rows in grouped.items():
        selected = valid_for_latency(rows) if valid_only else rows
        point = spread(selected, key)
        if point is None:
            continue
        center, lo, hi = point
        xs.append(qps)
        ys.append(center)
        low.append(lo)
        high.append(hi)
    if xs:
        ax.errorbar(
            xs,
            ys,
            yerr=[low, high],
            marker=marker,
            capsize=4,
            linewidth=1.6,
            label=label,
        )
    return len(xs)


def decorate(ax, title, ylabel):
    ax.set_title(title)
    ax.set_xlabel("Target submission rate (Pod QPS)")
    ax.set_ylabel(ylabel)
    ax.grid(alpha=0.3)


def save(fig, out_dir: Path, name: str):
    path = out_dir / name
    fig.savefig(path, dpi=160, bbox_inches="tight")
    plt.close(fig)
    print(f"wrote {path}")


def plot_validity(grouped, out_dir):
    xs = list(grouped)
    completion = [
        100 * (median(rows, "completion_rate") or 0) for rows in grouped.values()
    ]
    censored = [
        100 * (median(rows, "censored_rate") or 0) for rows in grouped.values()
    ]
    submitted = [
        100
        * (median(rows, "submitted") or 0)
        / (median(rows, "expected_pods") or 1)
        for rows in grouped.values()
    ]

    fig, ax = plt.subplots(figsize=(8, 4.8))
    ax.plot(xs, submitted, "o-", label="submitted / expected")
    ax.plot(xs, completion, "o-", label="completed / expected")
    ax.plot(xs, censored, "o-", label="censored")
    decorate(ax, "Experiment validity and completion", "Median across repeats (%)")
    ax.set_ylim(bottom=0)
    ax.axhline(100, color="0.5", linestyle="--", linewidth=1)
    ax.legend()
    save(fig, out_dir, "validity-vs-qps.png")


def plot_latency(grouped, out_dir):
    fig, axes = plt.subplots(1, 2, figsize=(12, 4.6), sharex=True)
    for key, label in (
        ("scheduling_p50_ms", "P50"),
        ("scheduling_p95_ms", "P95"),
        ("scheduling_p99_ms", "P99"),
    ):
        plot_series(axes[0], grouped, key, label)
    decorate(axes[0], "Pod scheduling latency", "Latency (ms)")
    axes[0].legend()

    for key, label in (
        ("e2e_p50_ms", "P50"),
        ("e2e_p95_ms", "P95"),
        ("e2e_p99_ms", "P99"),
    ):
        plot_series(axes[1], grouped, key, label)
    decorate(axes[1], "Pod end-to-end latency", "Latency (ms)")
    axes[1].legend()
    fig.suptitle("Only complete, uncensored runs are included")
    save(fig, out_dir, "latency-vs-qps.png")


def plot_after_submit(grouped, out_dir):
    fig, ax = plt.subplots(figsize=(8, 4.8))
    points = 0
    for key, label in (
        ("after_submit_p50_ms", "P50"),
        ("after_submit_p95_ms", "P95"),
        ("after_submit_p99_ms", "P99"),
    ):
        points += plot_series(ax, grouped, key, label)
    decorate(
        ax,
        "Gang latency after the final member was submitted",
        "t_group_ready - t_submit (ms)",
    )
    if points:
        ax.legend()
    else:
        ax.text(
            0.5,
            0.5,
            "No complete groups.csv samples",
            transform=ax.transAxes,
            ha="center",
        )
    save(fig, out_dir, "gang-after-submit-vs-qps.png")


def plot_bottlenecks(grouped, out_dir):
    fig, axes = plt.subplots(2, 2, figsize=(12, 8), sharex=True)
    panels = (
        ("prefilter_nodes_avg", "PreFilter nodes scanned", "Nodes/call"),
        ("registry_lock_wait_avg_us", "Registry lock wait", "Average (µs)"),
        ("permit_wait_avg_ms", "Permit semantic wait", "Average (ms)"),
        ("bind_avg_ms", "API Server Bind path", "Average (ms)"),
    )
    for ax, (key, title, unit) in zip(axes.flat, panels):
        plot_series(ax, grouped, key, "median, min/max", valid_only=True)
        decorate(ax, title, unit)
    axes[0][0].legend()
    fig.suptitle("Candidate bottleneck signals — complete, uncensored runs")
    fig.tight_layout()
    save(fig, out_dir, "bottleneck-signals-vs-qps.png")


AGGREGATE_FIELDS = [
    "target_qps",
    "runs",
    "complete_runs",
    "failed_status_runs",
    "completion_rate_median",
    "censored_rate_median",
    "scheduling_p95_ms_median",
    "scheduling_p99_ms_median",
    "e2e_p95_ms_median",
    "after_submit_p95_ms_median",
    "after_submit_p99_ms_median",
    "prefilter_nodes_avg_median",
    "registry_lock_wait_avg_us_median",
    "permit_wait_avg_ms_median",
    "waiting_iterated_avg_median",
    "gang_rejects_median",
    "bind_avg_ms_median",
]


def aggregate(grouped) -> list[dict]:
    output = []
    for qps, rows in grouped.items():
        complete = valid_for_latency(rows)
        source = complete
        output.append(
            {
                "target_qps": qps,
                "runs": len(rows),
                "complete_runs": len(complete),
                "failed_status_runs": sum(row.get("status") != "PASS" for row in rows),
                "completion_rate_median": median(rows, "completion_rate"),
                "censored_rate_median": median(rows, "censored_rate"),
                "scheduling_p95_ms_median": median(source, "scheduling_p95_ms"),
                "scheduling_p99_ms_median": median(source, "scheduling_p99_ms"),
                "e2e_p95_ms_median": median(source, "e2e_p95_ms"),
                "after_submit_p95_ms_median": median(source, "after_submit_p95_ms"),
                "after_submit_p99_ms_median": median(source, "after_submit_p99_ms"),
                "prefilter_nodes_avg_median": median(source, "prefilter_nodes_avg"),
                "registry_lock_wait_avg_us_median": median(
                    source, "registry_lock_wait_avg_us"
                ),
                "permit_wait_avg_ms_median": median(source, "permit_wait_avg_ms"),
                "waiting_iterated_avg_median": median(source, "waiting_iterated_avg"),
                "gang_rejects_median": median(source, "gang_rejects"),
                "bind_avg_ms_median": median(source, "bind_avg_ms"),
            }
        )
    return output


def write_aggregate(rows: list[dict], out_dir: Path):
    path = out_dir / "aggregate.csv"
    with path.open("w", newline="") as stream:
        writer = csv.DictWriter(stream, fieldnames=AGGREGATE_FIELDS)
        writer.writeheader()
        for row in rows:
            writer.writerow(
                {
                    key: (
                        f"{value:.6f}"
                        if isinstance(value, float) and not value.is_integer()
                        else value
                    )
                    for key, value in row.items()
                }
            )
    print(f"wrote {path}")


def ratio(current: float | None, baseline: float | None) -> float | None:
    if current is None or baseline is None or baseline <= 0:
        return None
    return current / baseline


def find_breakpoint(rows: list[dict]) -> tuple[dict | None, list[str]]:
    usable = [row for row in rows if row["complete_runs"] > 0]
    if not usable:
        return None, ["No QPS level has a complete, uncensored run."]
    baseline = usable[0]
    reasons = []
    for row in rows:
        row_reasons = []
        if (row["censored_rate_median"] or 0) > 0.01:
            row_reasons.append("median censored rate exceeded 1%")
        scheduling_ratio = ratio(
            row["scheduling_p95_ms_median"], baseline["scheduling_p95_ms_median"]
        )
        after_submit_ratio = ratio(
            row["after_submit_p95_ms_median"],
            baseline["after_submit_p95_ms_median"],
        )
        if scheduling_ratio is not None and scheduling_ratio >= 2:
            row_reasons.append(
                f"scheduling P95 reached {scheduling_ratio:.2f}× baseline"
            )
        if after_submit_ratio is not None and after_submit_ratio >= 2:
            row_reasons.append(
                f"post-submit Gang P95 reached {after_submit_ratio:.2f}× baseline"
            )
        if row_reasons:
            return row, row_reasons
    reasons.append(
        "No tested QPS crossed the default rule: censored >1% or P95 >=2× baseline."
    )
    return None, reasons


def candidate_signals(baseline: dict, point: dict) -> list[str]:
    candidates = []
    metrics = (
        ("prefilter_nodes_avg_median", "PreFilter scanning"),
        ("registry_lock_wait_avg_us_median", "Registry lock contention"),
        ("bind_avg_ms_median", "API Server Bind path"),
        ("waiting_iterated_avg_median", "Permit waiting-Pod iteration"),
    )
    for key, label in metrics:
        change = ratio(point.get(key), baseline.get(key))
        if change is not None and change >= 2:
            candidates.append(f"{label}: {change:.2f}× the baseline median")
    if (point.get("gang_rejects_median") or 0) > 0:
        candidates.append(
            f"Gang rejection: median {point['gang_rejects_median']:.0f} rejects/run"
        )
    return candidates


def fmt(value, digits=2):
    return "n/a" if value is None else f"{value:.{digits}f}"


def write_report(
    summary_path: Path, runs: list[dict], aggregated: list[dict], out_dir: Path
):
    batch_id = runs[0].get("batch_id") or summary_path.parent.name
    complete_runs = sum(row["data_complete"] for row in runs)
    status_failures = sum(row.get("status") != "PASS" for row in runs)
    partial_qps = [
        row["target_qps"]
        for row in aggregated
        if row["complete_runs"] < row["runs"]
    ]
    breakpoint, reasons = find_breakpoint(aggregated)
    usable = [row for row in aggregated if row["complete_runs"] > 0]
    baseline = usable[0] if usable else None

    lines = [
        f"# Exp2 controlled-load performance report — `{batch_id}`",
        "",
        f"- Source: `{summary_path}`",
        f"- Runs present: {len(runs)}",
        f"- Complete, uncensored runs: {complete_runs}",
        f"- Runs whose smoke status is FAIL: {status_failures}",
        f"- QPS levels present: {', '.join(fmt(row['target_qps'], 0) for row in aggregated)}",
        "",
        "## Validity",
        "",
    ]
    if partial_qps:
        lines.append(
            "Latency comparisons exclude incomplete/censored runs at QPS: "
            + ", ".join(fmt(qps, 0) for qps in partial_qps)
            + "."
        )
    else:
        lines.append("All recorded runs are complete and uncensored.")
    lines.extend(
        [
            "",
            "A `FAIL` smoke status is reported separately from data completeness: "
            "a metrics/log assertion can fail even when all Pod timelines are present.",
            "",
            "## QPS summary",
            "",
            "| QPS | runs | complete | completion | sched P95 | after-submit P95 | lock avg | bind avg | rejects |",
            "|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
        ]
    )
    for row in aggregated:
        lines.append(
            f"| {fmt(row['target_qps'], 0)} "
            f"| {row['runs']} "
            f"| {row['complete_runs']} "
            f"| {fmt((row['completion_rate_median'] or 0) * 100)}% "
            f"| {fmt(row['scheduling_p95_ms_median'])} ms "
            f"| {fmt(row['after_submit_p95_ms_median'])} ms "
            f"| {fmt(row['registry_lock_wait_avg_us_median'])} µs "
            f"| {fmt(row['bind_avg_ms_median'])} ms "
            f"| {fmt(row['gang_rejects_median'], 0)} |"
        )

    lines.extend(["", "## Automated breakpoint screen", ""])
    if breakpoint is None:
        lines.extend(reasons)
    else:
        lines.append(
            f"First flagged level: **QPS {fmt(breakpoint['target_qps'], 0)}**."
        )
        lines.extend(f"- {reason}" for reason in reasons)
        if baseline is not None:
            candidates = candidate_signals(baseline, breakpoint)
            lines.extend(["", "Signals that also changed at least 2×:"])
            if candidates:
                lines.extend(f"- {candidate}" for candidate in candidates)
            else:
                lines.append(
                    "- None of the recorded internal signals changed 2×; inspect CPU, "
                    "queueing, API Server metrics, and per-run logs."
                )

    lines.extend(
        [
            "",
            "## Interpretation limits",
            "",
            "- The screen identifies correlation and a candidate breakpoint, not causality.",
            "- Permit wait includes the intentional wait for remaining Gang members.",
            "- A resource-capacity failure can look like scheduler saturation; verify requested GPU does not exceed allocatable GPU.",
            "- Metrics are run-level deltas; use Prometheus only when second-by-second timing is needed.",
            "- Confirm a candidate bottleneck with a follow-up experiment that changes only the suspected dimension.",
            "",
        ]
    )
    path = out_dir / "report.md"
    path.write_text("\n".join(lines))
    print(f"wrote {path}")


def default_output(summary_path: Path, runs: list[dict]) -> Path:
    batch_id = runs[0].get("batch_id") or summary_path.parent.name
    return Path("analysis") / "exp2" / str(batch_id)


def parse_args():
    parser = argparse.ArgumentParser(
        description="Analyze an Exp2 controlled-load phase load-test summary and generate plots/report."
    )
    parser.add_argument(
        "--input",
        required=True,
        type=Path,
        help="Batch summary.csv, or its containing directory.",
    )
    parser.add_argument(
        "--out",
        type=Path,
        help="Output directory (default: analysis/exp2/<batch-id>).",
    )
    return parser.parse_args()


def main():
    args = parse_args()
    summary_path = args.input / "summary.csv" if args.input.is_dir() else args.input
    if not summary_path.is_file():
        sys.exit(f"input summary does not exist: {summary_path}")
    try:
        runs = load_runs(summary_path.resolve())
    except (OSError, ValueError) as error:
        sys.exit(str(error))

    grouped = group_runs(runs)
    if not grouped:
        sys.exit("no rows contain a numeric target_qps")

    out_dir = (args.out or default_output(summary_path, runs)).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    aggregated = aggregate(grouped)

    write_aggregate(aggregated, out_dir)
    plot_validity(grouped, out_dir)
    plot_latency(grouped, out_dir)
    plot_after_submit(grouped, out_dir)
    plot_bottlenecks(grouped, out_dir)
    write_report(summary_path.resolve(), runs, aggregated, out_dir)
    print(f"analysis complete: {out_dir}")


if __name__ == "__main__":
    main()
