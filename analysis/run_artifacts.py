"""Shared readers for experiment analysis scripts.

One run is identified by its ``*-meta.json`` file. All other artifacts use the
same prefix, for example ``pods-meta.json``, ``pods-summary.csv`` and
``pods-host.csv``.
"""

from __future__ import annotations

import csv
import json
import math
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path


def number(value):
    if value is None or isinstance(value, bool):
        return None
    try:
        result = float(value)
    except (TypeError, ValueError):
        return None
    return result if math.isfinite(result) else None


def timestamp(value) -> float | None:
    if value is None:
        return None
    text = str(value).strip().replace("Z", "+00:00")
    if not text:
        return None
    if "." in text:
        head, _, tail = text.partition(".")
        digits = 0
        while digits < len(tail) and tail[digits].isdigit():
            digits += 1
        text = f"{head}.{tail[:digits][:6]}{tail[digits:]}"
    try:
        return datetime.fromisoformat(text).timestamp()
    except ValueError:
        return None


def rows(path: Path | None) -> list[dict]:
    if path is None or not path.is_file():
        return []
    with path.open(newline="") as stream:
        return list(csv.DictReader(stream))


@dataclass(frozen=True)
class RunArtifacts:
    prefix: Path
    meta: dict

    @property
    def run_id(self) -> str:
        return str(self.meta.get("run_id") or self.prefix.name)

    def path(self, suffix: str) -> Path | None:
        candidate = self.prefix.with_name(self.prefix.name + suffix)
        return candidate if candidate.is_file() else None

    def summary(self) -> dict[str, dict[str, float]]:
        output = {}
        for row in rows(self.path("-summary.csv")):
            phase = (row.get("phase") or "").strip()
            if phase:
                output[phase] = {
                    key: value
                    for key, raw in row.items()
                    if key != "phase" and (value := number(raw)) is not None
                }
        return output

    def timeline(self) -> list[dict]:
        return rows(self.path(".csv"))

    def submitted_count(self) -> int | None:
        path = self.prefix.parent / "submit.jsonl"
        if not path.is_file():
            return None
        with path.open() as stream:
            return sum(bool(line.strip()) for line in stream)

    def timeline_stats(self) -> dict:
        timeline = self.timeline()
        submitted = [timestamp(row.get("submit_ts")) for row in timeline]
        scheduled = [timestamp(row.get("scheduled_ts")) for row in timeline]
        ready = [timestamp(row.get("ready_ts")) for row in timeline]
        submitted = [value for value in submitted if value is not None]
        scheduled = [value for value in scheduled if value is not None]
        ready = [value for value in ready if value is not None]
        censored = sum(
            (row.get("censored") or "").strip().lower() == "true" for row in timeline
        )
        scheduling_throughput = None
        if submitted and scheduled and max(scheduled) > min(submitted):
            scheduling_throughput = len(scheduled) / (max(scheduled) - min(submitted))
        return {
            "observed": len(timeline),
            "scheduled": len(scheduled),
            "ready": len(ready),
            "censored": censored,
            "submit_span_seconds": (
                max(submitted) - min(submitted) if len(submitted) >= 2 else 0.0
            ),
            "scheduling_throughput": scheduling_throughput,
        }

    def host_peaks(self) -> tuple[float | None, float | None]:
        host_rows = rows(self.path("-host.csv"))
        cpu = [value for row in host_rows if (value := number(row.get("cpu_percent"))) is not None]
        memory = [value for row in host_rows if (value := number(row.get("mem_mb"))) is not None]
        return (max(cpu) if cpu else None, max(memory) if memory else None)

    def apiserver_p99(self) -> tuple[float | None, float | None]:
        values = [
            value
            for row in rows(self.path("-apiserver.csv"))
            if (value := number(row.get("p99_ms"))) is not None
        ]
        if not values:
            return None, None
        return max(values), median(values)

    def pressure(self) -> dict[str, float | None]:
        output = {"http_429_total": None, "apf_inqueue_peak": None}
        for row in rows(self.path("-pressure.csv")):
            metric = (row.get("metric") or "").strip()
            if metric not in output:
                continue
            available = (row.get("available") or "true").strip().lower() == "true"
            output[metric] = number(row.get("value")) if available else None
        return output

    def prometheus_counter_delta(self, metric: str) -> float | None:
        values = [
            value
            for row in rows(self.path("-prometheus.csv"))
            if (row.get("metric") or "").strip() == metric
            and (value := number(row.get("value"))) is not None
        ]
        if not values:
            return None
        if len(values) == 1:
            return values[0]
        delta = values[-1] - values[0]
        return values[-1] if delta < 0 else delta


def discover_runs(root: Path, experiment: str) -> list[RunArtifacts]:
    if not root.is_dir():
        raise ValueError(f"input directory does not exist: {root}")
    output = []
    for meta_path in sorted(root.rglob("*-meta.json")):
        try:
            meta = json.loads(meta_path.read_text())
        except (OSError, json.JSONDecodeError) as error:
            raise ValueError(f"cannot read {meta_path}: {error}") from error
        if str(meta.get("experiment", "")).lower() != experiment.lower():
            continue
        prefix = meta_path.with_name(meta_path.name[: -len("-meta.json")])
        output.append(RunArtifacts(prefix=prefix, meta=meta))
    if not output:
        raise ValueError(f"no {experiment} *-meta.json runs found under {root}")
    return output


def median(values) -> float | None:
    usable = sorted(value for raw in values if (value := number(raw)) is not None)
    if not usable:
        return None
    middle = len(usable) // 2
    if len(usable) % 2:
        return usable[middle]
    return (usable[middle - 1] + usable[middle]) / 2


def spread(values) -> tuple[float, float, float] | None:
    usable = [value for raw in values if (value := number(raw)) is not None]
    center = median(usable)
    if center is None:
        return None
    return center, center - min(usable), max(usable) - center


def fmt(value, digits: int = 2) -> str:
    return "n/a" if value is None else f"{value:.{digits}f}"
