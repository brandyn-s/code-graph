"""Update fixtures.json SHA for code-graph-go to today's HEAD."""
import json
from pathlib import Path

p = Path(__file__).parent / "fixtures.json"
data = json.loads(p.read_text())
for fx in data.get("fixtures", []):
    if fx.get("id") == "code-graph-go":
        fx["sha"] = "1dab656f84135f8f6a448eb3066598c3f31f3fb5"
        fx["short_sha"] = "1dab656"
        break
p.write_text(json.dumps(data, indent=2))
print("updated")
