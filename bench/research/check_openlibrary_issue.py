"""Inspect openlibrary issue text + what new query selector produces."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent))
import pandas as pd
from eval_locbench_compare import pick_query_from_issue

df = pd.read_parquet("bench/research/locbench.parquet")
row = df[df["instance_id"] == "internetarchive__openlibrary-3196"].iloc[0]
print(f"Repo: {row['repo']}")
print(f"Category: {row['category']}")
print(f"Ground truth: {list(row['edit_functions'])}")
print()
print("=== problem_statement (first 3000 chars) ===")
print(row["problem_statement"][:3000])
print()
print("=== query the harness will pick ===")
q = pick_query_from_issue(row["problem_statement"])
print(q[:1500])
