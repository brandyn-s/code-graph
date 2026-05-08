// Pattern 3: fetch with a template literal that has a dynamic id
// slot at the end of the URL. Pre-C2 this was invisible to pathRe
// for the same reason as Pattern 2. C2's templateLiteralRe captures
// the backtick contents and pathInFormatRe extracts `/api/users/`
// (trailing slash trimmed to `/api/users`). matchAndLink pairs
// that against the axum route at exactly `/api/users` — the static
// prefix is the matchable surface.

export async function fetchUserById(id: number) {
  const r = await fetch(`/api/users/${id}`);
  return r.json();
}
