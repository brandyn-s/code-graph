# Index format versioning

Every project database under the cache directory is a SQLite file. Its
on-disk format is versioned with SQLite's `user_version` pragma:

| Constant | Value | Meaning |
|---|---|---|
| `store.FormatVersion` | 1 | Format this build writes and reads |
| `store.MinSupportedFormatVersion` | 1 | Oldest format this build still opens |

## Rules

- Opening a database stamped with a **higher** version than this build fails
  with `ErrIndexFormatTooNew` and the message names the remedy: upgrade
  code-graph, or delete the project and re-index.
- Opening a database stamped **below** `MinSupportedFormatVersion` fails with
  `ErrIndexFormatUnsupported` and asks for a rebuild with `index_repository`
  (or `delete_project` then re-index). Nothing is migrated in place.
- A database with `user_version` 0 is either fresh or was written before
  versioning existed. Both are treated as format 1 and stamped on open.
- `code-graph doctor` prints the format version of every project database.

## When to bump

Bump `FormatVersion` when a change makes databases written by this build
unreadable by the previous release: a new required column without a default,
a changed edge or node identity scheme, a changed meaning for an existing
column, or a change to the identity envelope that older readers would
misinterpret. Adding a nullable column with a default does not require a bump.
If a bump also drops support for reading an older format, raise
`MinSupportedFormatVersion` in the same change.

Every bump must:

1. Update this document with a row describing what changed and why older
   readers cannot cope.
2. Add a fixture database built at the new version under
   `internal/store/testdata/format-v<N>/` (index
   `bench/accuracy/synthetic/go-minimal` with the release binary and copy the
   checkpointed `.db` file; keep it under 200 KB).
3. Keep the previous fixture so `format_version_test.go` proves the older
   database is either still readable or refused with the rebuild message.
4. Note the bump in `CHANGELOG.md` under a "Breaking" heading.

## History

| Version | Introduced | Change |
|---|---|---|
| 1 | v0.9.0 | First stamped format. Identical to the unstamped databases written by v0.8.x, which are adopted on open. |
