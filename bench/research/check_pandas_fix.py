"""Verify the pandas StringMethods._validate is now extracted after PR #94."""
import sqlite3

DB = "file:C:/Users/user/.cache/code-graph/c-tmp-locbench-batch-pandas-dev__pandas-59900.db?mode=ro"

con = sqlite3.connect(DB, uri=True)
cur = con.cursor()

n = cur.execute(
    "SELECT COUNT(*) FROM nodes WHERE qualified_name LIKE '%stringmethods.%'"
).fetchone()[0]
print(f"StringMethods class methods (was 10): {n}")

print("\nStringMethods._validate (the original miss):")
for row in cur.execute(
    "SELECT label, name, qualified_name, file_path, start_line "
    "FROM nodes "
    "WHERE LOWER(qualified_name) LIKE '%stringmethods._validate'"
):
    print(f"  {row}")

print("\nFirst 20 methods on StringMethods (was 10 total before fix):")
for row in cur.execute(
    "SELECT label, name, start_line FROM nodes "
    "WHERE qualified_name LIKE '%stringmethods.%' AND label = 'Method' "
    "ORDER BY start_line LIMIT 20"
):
    print(f"  {row}")

n_file = cur.execute(
    "SELECT COUNT(*) FROM nodes WHERE file_path = 'pandas/core/strings/accessor.py'"
).fetchone()[0]
print(f"\nTotal nodes in accessor.py (was 41): {n_file}")
