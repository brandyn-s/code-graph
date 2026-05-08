// Exercises fetch() call extraction in TS/JSX.
//
// Today (pre-Phase C2): these calls are extracted as ordinary CALLS
// edges to a `fetch` symbol — no HTTP_CALLS classification.
// After Phase C2 ships: these become HTTP_CALLS edges with url_path
// metadata. The check_post_battery.py harness asserts the appropriate
// floor for the current code-graph build.

export async function fetchUsers() {
  const r = await fetch("/api/users");
  return r.json();
}

export async function postUser(name: string) {
  const r = await fetch("/api/users", {
    method: "POST",
    body: JSON.stringify({ name }),
  });
  return r.json();
}

export async function deleteUser(id: number) {
  const r = await fetch(`/api/users/${id}`, { method: "DELETE" });
  return r.ok;
}
