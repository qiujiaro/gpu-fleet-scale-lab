#!/usr/bin/env python3
"""Analyze paired Exp3 baseline/optimized burst runs.

Input is the Exp3 artifact tree containing one ``*-meta.json`` per arm/run.
Runs are paired by seed; unmatched or control-mismatched pairs remain visible
but are excluded from paired deltas.
"""

from __future__ import annotations

import argparse
import csv
import sys
from collections import defaultdict
from pathlib import Path

try:
    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
except ImportError:
    sys.exit("matplotlib is required: python -m pip install -r analysis/requirements.txt")

from run_artifacts import discover_runs, fmt, median, number, rows, spread, timestamp


ARMS = ("baseline", "optimized")


def normalized_arm(run) -> str:
    arm = str(run.meta.get("arm", "")).lower()
    if arm in ARMS:
        return arm
    optimize = run.meta.get("optimize")
    return "optimized" if optimize is True else "baseline" if optimize is False else arm


def run_record(run) -> dict:
    summary = run.summary()
    timeline = run.timeline_stats()
    host_cpu, host_memory = run.host_peaks()
    api_peak, api_median = run.apiserver_p99()
    pressure = run.pressure()
    create_requests = run.prometheus_counter_delta("pod_create_success_total")
    binding_requests = run.prometheus_counter_delta("pod_binding_success_total")
    expected = number(run.meta.get("expected_pods"))
    if expected is None:
        expected = number(run.meta.get("burst_pods"))
    if expected is None:
        expected = run.submitted_count()
    if expected is None:
        expected = 0
    valid = (
        run.meta.get("status") == "complete"
        and expected > 0
        and timeline["observed"] == expected
        and timeline["censored"] == 0
        and create_requests == expected
        and binding_requests == expected
    )
    return {
        "run_id": run.run_id,
        "arm": normalized_arm(run),
        "seed": number(run.meta.get("seed")),
        "nodes": number(run.meta.get("nodes")),
        "status": run.meta.get("status"),
        "arrival": run.meta.get("arrival"),
        "qps": number(run.meta.get("qps")),
        "workload_duration_seconds": number(run.meta.get("workload_duration_seconds")),
        "simulated_cold_start": run.meta.get("simulated_cold_start"),
        "expected_pods": expected,
        "observed": timeline["observed"],
        "censored": timeline["censored"],
        "submit_span_seconds": timeline["submit_span_seconds"],
        "burst_span_valid": timeline["submit_span_seconds"] <= 5,
        "valid": valid,
        "scheduling_p99_ms": summary.get("scheduling", {}).get("p99_ms"),
        "binding_p99_ms": summary.get("binding", {}).get("p99_ms"),
        "coldstart_p99_ms": summary.get("cold-start", {}).get("p99_ms"),
        "e2e_p99_ms": summary.get("end-to-end", {}).get("p99_ms"),
        "apiserver_p99_peak_ms": api_peak,
        "apiserver_p99_median_ms": api_median,
        "apf_inqueue_peak": pressure["apf_inqueue_peak"],
        "http_429_total": pressure["http_429_total"],
        "pod_create_success_total": create_requests,
        "pod_binding_success_total": binding_requests,
        "host_cpu_peak_percent": host_cpu,
        "host_mem_peak_mb": host_memory,
        "git_sha": run.meta.get("git_sha"),
        "git_dirty": run.meta.get("git_dirty"),
        "_run": run,
    }


def same_controls(left: dict, right: dict) -> bool:
    return all(
        left.get(key) == right.get(key)
        for key in (
            "seed",
            "nodes",
            "expected_pods",
            "arrival",
            "qps",
            "workload_duration_seconds",
            "simulated_cold_start",
            "git_sha",
        )
    )


def build_pairs(records: list[dict]) -> list[dict]:
    grouped = defaultdict(dict)
    for record in records:
        if record["seed"] is not None and record["arm"] in ARMS:
            grouped[int(record["seed"])][record["arm"]] = record
    output = []
    metrics = (
        "e2e_p99_ms",
        "binding_p99_ms",
        "apiserver_p99_peak_ms",
        "apf_inqueue_peak",
        "http_429_total",
        "pod_binding_success_total",
        "host_cpu_peak_percent",
    )
    for seed, arms in sorted(grouped.items()):
        baseline = arms.get("baseline")
        optimized = arms.get("optimized")
        valid = bool(
            baseline
            and optimized
            and baseline["valid"]
            and optimized["valid"]
            and baseline["burst_span_valid"]
            and optimized["burst_span_valid"]
            and same_controls(baseline, optimized)
        )
        row = {
            "seed": seed,
            "baseline_run_id": baseline["run_id"] if baseline else "",
            "optimized_run_id": optimized["run_id"] if optimized else "",
            "valid_pair": valid,
        }
        for metric in metrics:
            before = baseline.get(metric) if baseline else None
            after = optimized.get(metric) if optimized else None
            row[f"baseline_{metric}"] = before
            row[f"optimized_{metric}"] = after
            row[f"delta_{metric}"] = (
                after - before if before is not None and after is not None else None
            )
        output.append(row)
    return output


def aggregate(records, pairs):
    output = []
    for arm in ARMS:
        arm_records = [record for record in records if record["arm"] == arm]
        valid = [record for record in arm_records if record["valid"] and record["burst_span_valid"]]
        output.append(
            {
                "arm": arm,
                "runs": len(arm_records),
                "valid_runs": len(valid),
                "submit_span_seconds_median": median(r["submit_span_seconds"] for r in valid),
                "scheduling_p99_ms_median": median(r["scheduling_p99_ms"] for r in valid),
                "binding_p99_ms_median": median(r["binding_p99_ms"] for r in valid),
                "e2e_p99_ms_median": median(r["e2e_p99_ms"] for r in valid),
                "apiserver_p99_peak_ms_median": median(r["apiserver_p99_peak_ms"] for r in valid),
                "apf_inqueue_peak_median": median(r["apf_inqueue_peak"] for r in valid),
                "http_429_total_median": median(r["http_429_total"] for r in valid),
                "pod_binding_success_total_median": median(r["pod_binding_success_total"] for r in valid),
                "host_cpu_peak_percent_median": median(r["host_cpu_peak_percent"] for r in valid),
            }
        )
    valid_pairs = [pair for pair in pairs if pair["valid_pair"]]
    return output, valid_pairs


def write_csv(path, records):
    clean = [{key: value for key, value in record.items() if key != "_run"} for record in records]
    with path.open("w", newline="") as stream:
        writer = csv.DictWriter(stream, fieldnames=list(clean[0]))
        writer.writeheader()
        writer.writerows(clean)
    print(f"wrote {path}")


def plot_timeseries(records, out_dir):
    fig, ax = plt.subplots(figsize=(8, 4.8))
    plotted = 0
    colors = {"baseline": "tab:red", "optimized": "tab:green"}
    labeled = set()
    for record in records:
        if not record["valid"] or record["arm"] not in ARMS:
            continue
        points = [
            (timestamp(row.get("ts")), number(row.get("p99_ms")))
            for row in rows(record["_run"].path("-apiserver.csv"))
        ]
        points = [(ts, value) for ts, value in points if ts is not None and value is not None]
        if not points:
            continue
        points.sort()
        start = points[0][0]
        label = record["arm"] if record["arm"] not in labeled else None
        labeled.add(record["arm"])
        ax.plot(
            [ts - start for ts, _ in points],
            [value for _, value in points],
            color=colors[record["arm"]],
            alpha=0.65,
            label=label,
        )
        plotted += 1
    if plotted:
        ax.legend()
    else:
        ax.text(0.5, 0.5, "No valid API-server P99 series", transform=ax.transAxes, ha="center")
    ax.set_xlabel("Seconds since telemetry start")
    ax.set_ylabel("Pod-Create API-server P99 (ms)")
    ax.set_title("Exp3 — API-server P99 during burst")
    ax.grid(alpha=0.3)
    path = out_dir / "apiserver-p99-timeseries.png"
    fig.savefig(path, dpi=160, bbox_inches="tight")
    plt.close(fig)
    print(f"wrote {path}")


def plot_comparison(records, out_dir):
    valid = {
        arm: [record for record in records if record["arm"] == arm and record["valid"] and record["burst_span_valid"]]
        for arm in ARMS
    }
    panels = (
        ("e2e_p99_ms", "End-to-end P99", "ms"),
        ("binding_p99_ms", "Binding P99", "ms"),
        ("apiserver_p99_peak_ms", "API-server P99 peak", "ms"),
        ("apf_inqueue_peak", "APF queue peak", "requests"),
        ("http_429_total", "HTTP 429 delta", "requests"),
        ("host_cpu_peak_percent", "Host CPU peak", "%"),
    )
    fig, axes = plt.subplots(2, 3, figsize=(13, 8))
    for ax, (metric, title, unit) in zip(axes.flat, panels):
        labels, values, errors = [], [], []
        for arm in ARMS:
            point = spread(record[metric] for record in valid[arm])
            if point:
                labels.append(arm)
                values.append(point[0])
                errors.append(point[1:])
        if values:
            ax.bar(labels, values, yerr=list(map(list, zip(*errors))), capsize=4, color=["tab:red" if label == "baseline" else "tab:green" for label in labels])
        else:
            ax.text(0.5, 0.5, "unavailable", transform=ax.transAxes, ha="center")
        ax.set_title(title)
        ax.set_ylabel(unit)
        ax.grid(alpha=0.3, axis="y")
    fig.suptitle("Exp3 — baseline vs optimized (median, min/max)")
    fig.tight_layout()
    path = out_dir / "baseline-vs-optimized.png"
    fig.savefig(path, dpi=160, bbox_inches="tight")
    plt.close(fig)
    print(f"wrote {path}")


def write_report(path, source, records, aggregated, pairs, valid_pairs):
    missing_arms = [row["arm"] for row in aggregated if row["runs"] == 0]
    invalid_bursts = [r["run_id"] for r in records if not r["burst_span_valid"]]
    missing_pressure = sorted(
        {
            metric
            for record in records
            for metric in ("apf_inqueue_peak", "http_429_total")
            if record[metric] is None
        }
    )
    dirty = sum(record["git_dirty"] is True for record in records if record["valid"])
    shas = {record["git_sha"] for record in records if record["valid"] and record["git_sha"]}
    request_count_failures = [
        record["run_id"]
        for record in records
        if record["pod_create_success_total"] != record["expected_pods"]
        or record["pod_binding_success_total"] != record["expected_pods"]
    ]
    lines = [
        "# Exp3 paired burst report",
        "",
        f"- Source: `{source}`",
        f"- Runs discovered: {len(records)}",
        f"- Seed pairs discovered: {len(pairs)}",
        f"- Valid paired comparisons: {len(valid_pairs)}",
        f"- Formal requirement of three valid pairs met: {'yes' if len(valid_pairs) >= 3 else 'no'}",
        f"- Git SHAs among valid runs: {len(shas)}",
        f"- Dirty-worktree valid runs: {dirty}",
        f"- Missing arms: {', '.join(missing_arms) if missing_arms else 'none'}",
        f"- Runs whose Create span exceeds 5s: {', '.join(invalid_bursts) if invalid_bursts else 'none'}",
        f"- Runs violating Create/Binding request-count invariants: {', '.join(request_count_failures) if request_count_failures else 'none'}",
        f"- Unavailable pressure metrics: {', '.join(missing_pressure) if missing_pressure else 'none'}",
        "",
        "## Arm summary",
        "",
        "| arm | runs | valid | Create span | Bind P99 | API P99 peak | E2E P99 | Bind writes | APF peak | 429s |",
        "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for row in aggregated:
        lines.append(
            f"| {row['arm']} | {row['runs']} | {row['valid_runs']} "
            f"| {fmt(row['submit_span_seconds_median'])} s "
            f"| {fmt(row['binding_p99_ms_median'])} ms "
            f"| {fmt(row['apiserver_p99_peak_ms_median'])} ms "
            f"| {fmt(row['e2e_p99_ms_median'])} ms "
            f"| {fmt(row['pod_binding_success_total_median'], 0)} "
            f"| {fmt(row['apf_inqueue_peak_median'])} "
            f"| {fmt(row['http_429_total_median'], 0)} |"
        )
    lines += ["", "## Paired deltas (optimized - baseline)", ""]
    if not valid_pairs:
        lines.append("No valid pair is available; no optimization conclusion can be made.")
    else:
        lines += [
            f"- Median E2E P99 delta: {fmt(median(p['delta_e2e_p99_ms'] for p in valid_pairs))} ms",
            f"- Median Bind P99 delta: {fmt(median(p['delta_binding_p99_ms'] for p in valid_pairs))} ms",
            f"- Median API P99 peak delta: {fmt(median(p['delta_apiserver_p99_peak_ms'] for p in valid_pairs))} ms",
            f"- Median APF peak delta: {fmt(median(p['delta_apf_inqueue_peak'] for p in valid_pairs))}",
        ]
    lines += [
        "",
        "## Interpretation limits",
        "",
        "- Only complete, uncensored runs with a Create span no greater than five seconds enter comparisons.",
        "- Missing Prometheus series remain unavailable; they are never converted to zero.",
        "- A lower API-server peak is not sufficient if scheduler-local Bind wait or end-to-end P99 worsens.",
        "- Successful Create and Binding request counts must each equal successfully observed Pods.",
        "- Simulated cold start does not measure image pulls, runtimes, CNI, storage, or real GPUs.",
        "",
    ]
    path.write_text("\n".join(lines))
    print(f"wrote {path}")


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--input", type=Path, required=True, help="Exp3 artifact root")
    parser.add_argument("--out", type=Path, help="default: analysis/exp3/<input-name>")
    args = parser.parse_args()
    try:
        runs = discover_runs(args.input.resolve(), "exp3")
    except ValueError as error:
        sys.exit(str(error))
    records = [run_record(run) for run in runs]
    unknown = sorted({record["arm"] for record in records if record["arm"] not in ARMS})
    if unknown:
        sys.exit(f"unknown Exp3 arms in meta.json: {unknown}")
    pairs = build_pairs(records)
    aggregated, valid_pairs = aggregate(records, pairs)
    out_dir = (args.out or Path("analysis") / "exp3" / args.input.name).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    write_csv(out_dir / "runs.csv", records)
    write_csv(out_dir / "aggregate.csv", aggregated)
    if pairs:
        write_csv(out_dir / "pairs.csv", pairs)
    plot_timeseries(records, out_dir)
    plot_comparison(records, out_dir)
    write_report(out_dir / "report.md", args.input.resolve(), records, aggregated, pairs, valid_pairs)
    print(f"analysis complete: {out_dir}")


if __name__ == "__main__":
    main()
