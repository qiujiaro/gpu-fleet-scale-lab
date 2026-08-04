#!/usr/bin/env python3
"""Analyze the catalog Exp1 control-plane scale sweep.

Input is the Exp1 artifact tree containing one ``*-meta.json`` per run. Outputs
are written to ``analysis/exp1/<input-directory-name>`` by default.
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

from run_artifacts import discover_runs, fmt, median, number, spread


EXPECTED_NODES = (100, 500, 1000, 1500, 2000)


def run_record(run) -> dict:
    summary = run.summary()
    timeline = run.timeline_stats()
    host_cpu, host_memory = run.host_peaks()
    api_peak, api_median = run.apiserver_p99()
    pressure = run.pressure()
    create_requests = run.prometheus_counter_delta("pod_create_success_total")
    expected = number(run.meta.get("expected_pods"))
    observed = timeline["observed"]
    censored = timeline["censored"]
    complete = (
        run.meta.get("status") == "complete"
        and expected is not None
        and observed == expected
        and censored == 0
        and create_requests == expected
    )
    return {
        "run_id": run.run_id,
        "nodes": number(run.meta.get("nodes")),
        "status": run.meta.get("status"),
        "scheduler": run.meta.get("scheduler"),
        "arrival": run.meta.get("arrival"),
        "qps": number(run.meta.get("qps")),
        "seed": number(run.meta.get("seed")),
        "expected_pods": expected,
        "observed": observed,
        "censored": censored,
        "valid": complete,
        "scheduling_p95_ms": summary.get("scheduling", {}).get("p95_ms"),
        "scheduling_p99_ms": summary.get("scheduling", {}).get("p99_ms"),
        "e2e_p99_ms": summary.get("end-to-end", {}).get("p99_ms"),
        "throughput_pods_s": timeline["scheduling_throughput"],
        "apiserver_p99_peak_ms": api_peak,
        "apiserver_p99_median_ms": api_median,
        "apf_inqueue_peak": pressure["apf_inqueue_peak"],
        "http_429_total": pressure["http_429_total"],
        "pod_create_success_total": create_requests,
        "host_cpu_peak_percent": host_cpu,
        "host_mem_peak_mb": host_memory,
        "git_sha": run.meta.get("git_sha"),
        "git_dirty": run.meta.get("git_dirty"),
    }


def aggregate(records: list[dict]) -> list[dict]:
    grouped = defaultdict(list)
    for record in records:
        if record["nodes"] is not None:
            grouped[int(record["nodes"])].append(record)
    output = []
    for nodes, group in sorted(grouped.items()):
        valid = [record for record in group if record["valid"]]
        output.append(
            {
                "nodes": nodes,
                "runs": len(group),
                "valid_runs": len(valid),
                "failed_runs": len(group) - len(valid),
                "scheduling_p95_ms_median": median(r["scheduling_p95_ms"] for r in valid),
                "scheduling_p99_ms_median": median(r["scheduling_p99_ms"] for r in valid),
                "e2e_p99_ms_median": median(r["e2e_p99_ms"] for r in valid),
                "throughput_pods_s_median": median(r["throughput_pods_s"] for r in valid),
                "apiserver_p99_peak_ms_median": median(r["apiserver_p99_peak_ms"] for r in valid),
                "apf_inqueue_peak_median": median(r["apf_inqueue_peak"] for r in valid),
                "http_429_total_median": median(r["http_429_total"] for r in valid),
                "host_cpu_peak_percent_median": median(r["host_cpu_peak_percent"] for r in valid),
                "host_mem_peak_mb_median": median(r["host_mem_peak_mb"] for r in valid),
            }
        )
    return output


def write_csv(path: Path, records: list[dict]):
    with path.open("w", newline="") as stream:
        writer = csv.DictWriter(stream, fieldnames=list(records[0]))
        writer.writeheader()
        writer.writerows(records)
    print(f"wrote {path}")


def plot_latency_throughput(records, out_dir):
    grouped = defaultdict(list)
    for record in records:
        if record["valid"]:
            grouped[int(record["nodes"])].append(record)
    xs, latency, latency_err, throughput, throughput_err = [], [], [], [], []
    for nodes, group in sorted(grouped.items()):
        latency_point = spread(r["scheduling_p99_ms"] for r in group)
        throughput_point = spread(r["throughput_pods_s"] for r in group)
        if latency_point and throughput_point:
            xs.append(nodes)
            latency.append(latency_point[0])
            latency_err.append(latency_point[1:])
            throughput.append(throughput_point[0])
            throughput_err.append(throughput_point[1:])
    fig, left = plt.subplots(figsize=(8, 4.8))
    if xs:
        left.errorbar(xs, latency, yerr=list(map(list, zip(*latency_err))), marker="o", capsize=4, color="tab:red")
    left.set_xlabel("Ready simulated nodes")
    left.set_ylabel("Scheduling P99 (ms)", color="tab:red")
    left.grid(alpha=0.3)
    right = left.twinx()
    if xs:
        right.errorbar(xs, throughput, yerr=list(map(list, zip(*throughput_err))), marker="s", capsize=4, color="tab:green")
    right.set_ylabel("Scheduled Pods/s", color="tab:green")
    left.set_title("Exp1 — scheduling latency and throughput vs fleet size")
    path = out_dir / "latency-throughput-vs-nodes.png"
    fig.savefig(path, dpi=160, bbox_inches="tight")
    plt.close(fig)
    print(f"wrote {path}")


def plot_environment(records, out_dir):
    grouped = defaultdict(list)
    for record in records:
        if record["valid"]:
            grouped[int(record["nodes"])].append(record)
    xs = sorted(grouped)
    cpu = [median(r["host_cpu_peak_percent"] for r in grouped[n]) for n in xs]
    memory = [median(r["host_mem_peak_mb"] for r in grouped[n]) for n in xs]
    api = [median(r["apiserver_p99_peak_ms"] for r in grouped[n]) for n in xs]
    fig, axes = plt.subplots(1, 2, figsize=(12, 4.6))
    axes[0].plot(xs, cpu, "o-", label="host CPU peak (%)")
    axes[0].plot(xs, [v / 1024 if v is not None else float("nan") for v in memory], "s-", label="host RSS peak (GiB)")
    axes[0].set_title("Simulation-host pressure")
    axes[0].set_xlabel("Ready simulated nodes")
    axes[0].legend()
    axes[0].grid(alpha=0.3)
    axes[1].plot(xs, api, "o-", color="tab:purple")
    axes[1].set_title("API-server Pod-Create P99 peak")
    axes[1].set_xlabel("Ready simulated nodes")
    axes[1].set_ylabel("P99 (ms)")
    axes[1].grid(alpha=0.3)
    path = out_dir / "apiserver-host-vs-nodes.png"
    fig.savefig(path, dpi=160, bbox_inches="tight")
    plt.close(fig)
    print(f"wrote {path}")


def write_report(path, source, records, aggregated):
    present = {int(row["nodes"]) for row in aggregated}
    missing = [nodes for nodes in EXPECTED_NODES if nodes not in present]
    incomplete = [row for row in aggregated if row["valid_runs"] < 3]
    valid_records = [record for record in records if record["valid"]]
    shas = {record["git_sha"] for record in valid_records if record["git_sha"]}
    dirty = sum(record["git_dirty"] is True for record in valid_records)
    expected_controls = {
        "scheduler": "default-scheduler",
        "arrival": "constant",
        "qps": 50.0,
        "seed": 42.0,
        "expected_pods": 3000.0,
    }
    control_violations = []
    for key, expected in expected_controls.items():
        observed = sorted({record[key] for record in records if record[key] is not None}, key=str)
        if observed != [expected]:
            control_violations.append(f"{key}: observed {observed}, expected [{expected}]")
    lines = [
        "# Exp1 control-plane scale-sweep report",
        "",
        f"- Source: `{source}`",
        f"- Runs discovered: {len(records)}",
        f"- Valid, complete runs: {len(valid_records)}",
        f"- Git SHAs among valid runs: {len(shas)}",
        f"- Dirty-worktree valid runs: {dirty}",
        "",
        "## Matrix validity",
        "",
        f"- Missing node cells: {', '.join(map(str, missing)) if missing else 'none'}",
        f"- Cells with fewer than three valid repeats: {', '.join(str(r['nodes']) for r in incomplete) if incomplete else 'none'}",
        f"- Fixed-control violations: {'; '.join(control_violations) if control_violations else 'none'}",
        "",
        "## Aggregated results",
        "",
        "| nodes | runs | valid | sched P99 | throughput | API P99 peak | host CPU peak | host RSS peak |",
        "|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for row in aggregated:
        lines.append(
            f"| {row['nodes']} | {row['runs']} | {row['valid_runs']} "
            f"| {fmt(row['scheduling_p99_ms_median'])} ms "
            f"| {fmt(row['throughput_pods_s_median'])} Pods/s "
            f"| {fmt(row['apiserver_p99_peak_ms_median'])} ms "
            f"| {fmt(row['host_cpu_peak_percent_median'])}% "
            f"| {fmt((row['host_mem_peak_mb_median'] or 0) / 1024)} GiB |"
        )
    lines += [
        "",
        "## Interpretation limits",
        "",
        "- Invalid or censored runs are excluded from latency and throughput aggregation.",
        "- Host saturation is a confounder, not a Kubernetes control-plane scalability result.",
        "- The report describes this KWOK simulation and host; it does not measure real kubelets, GPUs, networking, or storage.",
        "",
    ]
    path.write_text("\n".join(lines))
    print(f"wrote {path}")


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--input", type=Path, required=True, help="Exp1 artifact root")
    parser.add_argument("--out", type=Path, help="default: analysis/exp1/<input-name>")
    args = parser.parse_args()
    try:
        runs = discover_runs(args.input.resolve(), "exp1")
    except ValueError as error:
        sys.exit(str(error))
    records = [run_record(run) for run in runs]
    if not any(record["nodes"] is not None for record in records):
        sys.exit("Exp1 runs do not declare numeric nodes in meta.json")
    aggregated = aggregate(records)
    out_dir = (args.out or Path("analysis") / "exp1" / args.input.name).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    write_csv(out_dir / "runs.csv", records)
    write_csv(out_dir / "aggregate.csv", aggregated)
    plot_latency_throughput(records, out_dir)
    plot_environment(records, out_dir)
    write_report(out_dir / "report.md", args.input.resolve(), records, aggregated)
    print(f"analysis complete: {out_dir}")


if __name__ == "__main__":
    main()
