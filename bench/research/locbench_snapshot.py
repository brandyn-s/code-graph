"""Cumulative accuracy snapshots from a live Loc-Bench run log.

Each completed instance prints `file=Y/N class=Y/N func=Y/N (Ns, Ntok $cost)`.
This parser computes accuracy at requested checkpoints (n=50, 100, 250, 400, 560).
Run repeatedly while the job progresses; later runs include more milestones.

Usage:
    python bench/research/locbench_snapshot.py /tmp/locbench-n560/run.log
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

CHECKPOINTS = [50, 100, 250, 400, 560]
LINE_RE = re.compile(
    r"^file=(?P<f>[YN]) class=(?P<c>[YN]) func=(?P<fu>[YN])"
    r" \((?P<sec>\d+)s, [^$]+\$(?P<cost>[0-9.]+)\)"
)


def parse(log_path: Path) -> list[dict]:
    rows = []
    for line in log_path.read_text(encoding="utf-8", errors="replace").splitlines():
        m = LINE_RE.match(line.strip())
        if m:
            rows.append({
                "file_hit": m.group("f") == "Y",
                "class_hit": m.group("c") == "Y",
                "func_hit": m.group("fu") == "Y",
                "sec": int(m.group("sec")),
                "cost": float(m.group("cost")),
            })
    return rows


def snapshot(rows: list[dict], n: int) -> dict:
    sub = rows[:n]
    if not sub:
        return {"n": n, "completed": 0}
    total = len(sub)
    return {
        "n": n,
        "completed": total,
        "file_pct": 100 * sum(r["file_hit"] for r in sub) / total,
        "class_pct": 100 * sum(r["class_hit"] for r in sub) / total,
        "func_pct": 100 * sum(r["func_hit"] for r in sub) / total,
        "total_cost": sum(r["cost"] for r in sub),
        "avg_sec": sum(r["sec"] for r in sub) / total,
    }


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: locbench_snapshot.py <run.log>", file=sys.stderr)
        return 2
    rows = parse(Path(sys.argv[1]))
    print(f"Total instances scored so far: {len(rows)}")
    print()
    print(f"{'n':>5} {'have':>6} {'file%':>7} {'class%':>7} {'func%':>7} {'cost':>7} {'avg_s':>6}")
    print("-" * 50)
    for n in CHECKPOINTS:
        s = snapshot(rows, n)
        if s["completed"] == 0:
            print(f"{n:>5} {'0':>6} {'-':>7} {'-':>7} {'-':>7} {'-':>7} {'-':>6}")
        elif s["completed"] < n:
            print(f"{n:>5} {s['completed']:>6} (in progress; {n - s['completed']} more for full checkpoint)")
        else:
            print(f"{n:>5} {n:>6} {s['file_pct']:>6.1f}% {s['class_pct']:>6.1f}% "
                  f"{s['func_pct']:>6.1f}% ${s['total_cost']:>5.2f} {s['avg_sec']:>5.1f}")

    if rows:
        cur = snapshot(rows, len(rows))
        print()
        print(f"Cumulative @ n={cur['completed']}: file={cur['file_pct']:.1f}% "
              f"class={cur['class_pct']:.1f}% func={cur['func_pct']:.1f}% "
              f"cost=${cur['total_cost']:.2f} avg={cur['avg_sec']:.1f}s")
    return 0


if __name__ == "__main__":
    sys.exit(main())
