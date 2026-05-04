"""Summarize Loc-Bench source-repo distribution and emit corpus mining plan.

Reads the previously-emitted full JSON output from enumerate_locbench_repos.py
on stdin (or hardcoded path), and prints a top-N summary table + a mining
plan (issue-number exclusion list per repo).
"""

import json
from pathlib import Path


def main() -> None:
    enum_out = Path(__file__).parent / "locbench_repos.json"
    data = json.loads(enum_out.read_text(encoding="utf-8"))

    total = data["total_instances"]
    print(f"Total: {total} instances, {data['distinct_repos']} distinct repos\n")

    # Top 30 with cumulative coverage
    print(f"{'Rank':>4} {'Repo':<42} {'Inst':>5} {'Cum%':>6}")
    print("-" * 60)
    cum = 0
    for i, r in enumerate(data["repos_by_instance_count"][:30]):
        cum += r["instance_count"]
        print(f"{i+1:>4} {r['repo']:<42} {r['instance_count']:>5} {cum/total*100:>5.1f}%")

    # Coverage at various N
    print("\nCoverage by top-N:")
    for n in [10, 15, 20, 30, 50, 100]:
        cum = sum(r["instance_count"] for r in data["repos_by_instance_count"][:n])
        print(f"  Top-{n:>3}: {cum:>4}/{total} = {cum/total*100:>5.1f}%")

    # Sample eval-set issue IDs per repo (top 15)
    print("\nSample eval_issue identifiers per top-15 repo (for exclusion at retrieval time):")
    eval_issues = data.get("eval_issues_per_repo", {})
    for r in data["repos_by_instance_count"][:15]:
        repo = r["repo"]
        ids = eval_issues.get(repo, [])
        sample = ids[:3] if len(ids) > 3 else ids
        print(f"  {repo:<40} ({len(ids):>3} eval IDs) — sample: {sample}")


if __name__ == "__main__":
    main()
