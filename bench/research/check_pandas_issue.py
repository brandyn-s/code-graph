"""Inspect pandas-dev__pandas-59900 issue text to understand why agent missed."""
import pandas as pd

df = pd.read_parquet("bench/research/locbench.parquet")
row = df[df["instance_id"] == "pandas-dev__pandas-59900"].iloc[0]
print(f"Repo: {row['repo']}")
print(f"Category: {row['category']}")
print(f"Ground truth: {list(row['edit_functions'])}")
print()
print("=== problem_statement (full) ===")
print(row["problem_statement"][:2000])
print()
print("=== first paragraph (what harness uses as query) ===")
print(row["problem_statement"].split("\n\n")[0])
