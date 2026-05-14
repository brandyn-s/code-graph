# cypher-semantics fixture

Purpose: pin the Cypher engine's array-property operators against regression.

Shipped 2026-05-13 alongside PR #308 (CONTAINS-on-array element-of semantics).
The 2026-05-13 PSM tool-comparison battery surfaced that the deployed binary
didn't yet have PR #308's fix — `WHERE decorators CONTAINS '#[test]'` returned
0 instead of 1,676. Once the binary was rebuilt, the same query returned 1,676.
This fixture + `bench/accuracy/check_cypher_semantics.py` guards against that
same regression at PR time.

## Element-of semantics

`CONTAINS` on an array property tests **element equality**, not substring-in-
element. The fixture's `gated_function` is decorated with
`#[cfg(feature = "experimental")]` — the full attribute including the `#[`
prefix is stored as a single array element. To match it via `CONTAINS`, the
operand must be the full element string, not a substring.

`param_types` and `return_types` arrays store the outer type constructor as
the element. A parameter typed `String` produces element `"String"`; a return
type of `Vec<String>` produces element `"Vec"`. The check exercises both.

## Files

- `Cargo.toml` — minimal package declaration so tree-sitter / extractor sees
  this as a Rust crate.
- `src/lib.rs` — fixture functions, each hand-authored to land specific
  values in array-typed node properties.
- `ground_truth.json` — minimum acceptable counts. Floors only; the extractor
  may add to these.

## Run locally

```bash
make build
python bench/accuracy/check_cypher_semantics.py
```

Exit 0 = pass. Exit 1 = at least one operator returned fewer rows than the
floor. Exit 2 = infrastructure failure.
