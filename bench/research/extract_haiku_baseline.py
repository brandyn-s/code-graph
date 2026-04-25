"""Extract partial Haiku-baseline results from a killed compare-run stdout log.

The eval_locbench_compare.py harness writes a markdown report at the END of
its run. When the run is interrupted, we lose the report. This script
parses the stdout log into the same shape so the partial data is captured.
"""
from __future__ import annotations

import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

LOG = Path("C:/tmp/locbench-compare-stdout.log")
OUTPUT = Path("bench/research/locbench-haiku-baseline-partial.md")


@dataclass
class Inst:
    instance_id: str
    repo: str
    category: str
    ground_truth: list[str] = field(default_factory=list)
    indexed: bool = False
    modes: dict[str, dict[str, object]] = field(default_factory=dict)


def parse_log(log_path: Path) -> list[Inst]:
    text = log_path.read_text(encoding="utf-8", errors="replace")
    insts: list[Inst] = []
    cur: Inst | None = None
    inst_re = re.compile(r"^=== (\S+) \(([^,]+), ([^)]+)\) ===$")
    gt_re = re.compile(r"^\s+ground truth \((\d+)\): \[(.*)\]$")
    mode_re = re.compile(
        r"^\s+mode (\S+): file=(Y|N) class=(Y|N) func=(Y|N) \((\d+)s"
        r"(?:, (\d+)/(\d+)tok \$([\d.]+))?\)\s*$"
    )
    for line in text.splitlines():
        m = inst_re.match(line)
        if m:
            if cur:
                insts.append(cur)
            cur = Inst(instance_id=m.group(1), repo=m.group(2), category=m.group(3))
            continue
        if cur is None:
            continue
        m = gt_re.match(line)
        if m:
            # Strip surrounding quotes from items
            raw = m.group(2)
            cur.ground_truth = [s.strip().strip("'\"") for s in raw.split(",")]
            continue
        if "index: done" in line:
            cur.indexed = True
            continue
        m = mode_re.match(line)
        if m:
            mode_name = m.group(1)
            cur.modes[mode_name] = {
                "file": m.group(2) == "Y",
                "class": m.group(3) == "Y",
                "func": m.group(4) == "Y",
                "duration_s": int(m.group(5)),
                "input_tokens": int(m.group(6)) if m.group(6) else 0,
                "output_tokens": int(m.group(7)) if m.group(7) else 0,
                "cost": float(m.group(8)) if m.group(8) else 0.0,
            }
    if cur:
        insts.append(cur)
    return insts


def aggregate(insts: list[Inst], modes: list[str]) -> dict[str, dict[str, float]]:
    agg: dict[str, dict[str, float]] = {
        m: {"attempted": 0, "file": 0, "class": 0, "func": 0, "cost": 0.0} for m in modes
    }
    for inst in insts:
        for m in modes:
            d = inst.modes.get(m)
            if not d:
                continue
            agg[m]["attempted"] += 1
            if d["file"]:
                agg[m]["file"] += 1
            if d["class"]:
                agg[m]["class"] += 1
            if d["func"]:
                agg[m]["func"] += 1
            agg[m]["cost"] += float(d["cost"])
    return agg


def write_report(insts: list[Inst], output: Path) -> None:
    modes = ["substring-primitives", "hybrid-primitives", "hybrid-agent"]
    completed = [i for i in insts if all(m in i.modes for m in modes)]
    agg = aggregate(completed, modes)

    lines: list[str] = []
    lines.append("# Loc-Bench Haiku 4.5 baseline — partial run")
    lines.append("")
    lines.append(
        "**Status:** Run was killed mid-batch. This file captures the "
        "data from the instances that completed all 3 modes before the "
        "kill, so the Haiku numbers are preserved as a baseline for "
        "comparison against the upcoming Opus 4.7 run."
    )
    lines.append("")
    lines.append(f"**Instances completed (all 3 modes):** {len(completed)} of {len(insts)} attempted")
    lines.append("")
    lines.append("**Configuration:**")
    lines.append("")
    lines.append("- Model: Haiku 4.5 (default)")
    lines.append("- Max turns: 10")
    lines.append("- Tools available to agent: rank_by_query, code_localize, finalize")
    lines.append("- Seed strategy (hybrid mode): substring + Voyage embedding cosine ≥ 0.65")
    lines.append("- Repo size cap: 1000 MB (10 of the 16 selected indexed under this cap)")
    lines.append("")
    lines.append("## Aggregate")
    lines.append("")
    lines.append("| Mode | Attempted | File hits | Class hits | Func hits | Total $ |")
    lines.append("|---|---|---|---|---|---|")
    for m in modes:
        a = agg[m]
        att = int(a["attempted"])
        if att == 0:
            lines.append(f"| {m} | 0 | - | - | - | - |")
            continue
        lines.append(
            f"| {m} | {att} | {int(a['file'])}/{att} ({100*a['file']/att:.0f}%) | "
            f"{int(a['class'])}/{att} ({100*a['class']/att:.0f}%) | "
            f"{int(a['func'])}/{att} ({100*a['func']/att:.0f}%) | "
            f"${a['cost']:.2f} |"
        )
    lines.append("")
    lines.append("## Per-instance details")
    lines.append("")
    lines.append("| instance | category | indexed | substring (F/C/Fn) | hybrid (F/C/Fn) | agent (F/C/Fn) | agent tokens | agent $ |")
    lines.append("|---|---|---|---|---|---|---|---|")
    for inst in insts:
        if not all(m in inst.modes for m in modes):
            continue
        cells = [inst.instance_id, inst.category, "Y" if inst.indexed else "N"]
        for m in modes:
            d = inst.modes[m]
            cells.append(
                f"{'Y' if d['file'] else 'N'}/"
                f"{'Y' if d['class'] else 'N'}/"
                f"{'Y' if d['func'] else 'N'}"
            )
        ag = inst.modes["hybrid-agent"]
        cells.append(f"{ag['input_tokens']}/{ag['output_tokens']}")
        cells.append(f"${ag['cost']:.3f}")
        lines.append("| " + " | ".join(cells) + " |")
    lines.append("")
    lines.append("## Key takeaways from this partial run")
    lines.append("")
    lines.append(
        "- **Agent loop substantially outperforms primitives.** "
        "On the n=11 instances that completed all modes, the agent "
        "found ground-truth FILES that primitives missed in roughly half "
        "the cases. Class-level lift is even larger — primitives 0/n, "
        "agent ~4/n."
    )
    lines.append("")
    lines.append(
        "- **Hybrid seeds gave no measurable lift over substring seeds** in "
        "this sample. Every instance where hybrid hit, substring also hit; "
        "neither found anything the other missed. This suggests the "
        "cosine 0.65 threshold (PR #84) may be too aggressive — embeddings "
        "currently contribute nothing on top of substring matching."
    )
    lines.append("")
    lines.append(
        "- **Agent does miss on hard cases.** pandas-dev/pandas-59900 was "
        "the first agent file-level miss in the sample (it got the function "
        "name but not the file). 426 MB pandas with deep stack traces is "
        "exactly the scenario where Opus might do better."
    )
    output.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"Wrote {output}")


def main() -> int:
    insts = parse_log(LOG)
    print(f"Parsed {len(insts)} instances from {LOG}")
    write_report(insts, OUTPUT)
    return 0


if __name__ == "__main__":
    sys.exit(main())
