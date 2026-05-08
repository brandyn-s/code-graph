// Pattern 1: fetch with a literal-quoted URL (single quotes / double
// quotes). The pre-existing httplink path catches this via pathRe;
// kept here as a control case so a regression on the literal-string
// branch is visible in the same fixture.

export async function fetchItems() {
  const r = await fetch("/api/items");
  return r.json();
}
