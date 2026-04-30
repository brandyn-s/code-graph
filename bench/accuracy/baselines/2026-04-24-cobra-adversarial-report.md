# code-graph accuracy baseline — cobra-adversarial

- **Date**: 2026-04-24
- **Fixture SHA**: `f2878bab8c96afd6e36968af96343b35dbb82a82` (short: `f2878ba`)
- **Project name**: `c-Users-user-Documents-bench-fixtures-cobra`

## Summary

Four metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's analyzed-caller set. Filters out scope-mismatch artifacts (e.g., code-graph edges from test files PyCG never reached).
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|
| CALLS | go-ast | 776 / 2128 | 0.350 / 0.959 / 0.512 | 0.421 / 0.959 / 0.585 | 0.421 / 0.959 / 0.585 |
| IMPORTS | go-ast (dropped) | 0 / 0 | — (Go oracle drops IMPORTS until import-path -> internal-file-Q) | — | — |

## Per-project scope-aligned F1

Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.

### CALLS

| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| cobra | 776 / 2128 | 744 | 1022 | 32 | 0.421 | 0.959 | **0.585** |

**Spread**: min F1 = 0.585, max F1 = 0.585, range = 0.000


## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 384

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  c-Users-user-Documents-bench-fixtures-cobra.active_help.GetActiveHelpConfig --> c-Users-user-Documents-bench-fixtures-cobra.command.Root
  c-Users-user-Documents-bench-fixtures-cobra.active_help.GetActiveHelpConfig --> os.Getenv
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpAlone --> c-Users-user-Documents-bench-fixtures-cobra.command.AddCommand
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpAlone --> fmt.Sprintf
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpAlone --> strings.Join
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpForFlag --> c-Users-user-Documents-bench-fixtures-cobra.command.Flags
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpForFlag --> c-Users-user-Documents-bench-fixtures-cobra.completions.RegisterFlagCompletionFunc
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpForFlag --> fmt.Sprintf
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpForFlag --> strings.Join
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpWithComps --> c-Users-user-Documents-bench-fixtures-cobra.command.AddCommand
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  c-Users-user-Documents-bench-fixtures-cobra.bash_completions_test.TestBashCompletions --> c-Users-user-Documents-bench-fixtures-cobra.completions_test.String
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.genMan --> c-Users-user-Documents-bench-fixtures-cobra.command.CommandPath
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPreamble --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintFlags --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.command.InheritedFlags
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.command.NonInheritedFlags
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs_test.TestGenManDoc --> c-Users-user-Documents-bench-fixtures-cobra.command_test.checkStringContains
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs_test.TestGenManDoc --> c-Users-user-Documents-bench-fixtures-cobra.command_test.checkStringOmits
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs_test.TestGenManNoGenTag --> c-Users-user-Documents-bench-fixtures-cobra.command_test.checkStringOmits
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  c-Users-user-Documents-bench-fixtures-cobra.active_help.AppendActiveHelp --> fmt.Sprintf
  c-Users-user-Documents-bench-fixtures-cobra.active_help.GetActiveHelpConfig --> c-Users-user-Documents-bench-fixtures-cobra.command.Root
  c-Users-user-Documents-bench-fixtures-cobra.active_help.GetActiveHelpConfig --> os.Getenv
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpAlone --> c-Users-user-Documents-bench-fixtures-cobra.command.AddCommand
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpAlone --> fmt.Sprintf
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpAlone --> strings.Join
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpForFlag --> c-Users-user-Documents-bench-fixtures-cobra.command.Flags
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpForFlag --> c-Users-user-Documents-bench-fixtures-cobra.completions.RegisterFlagCompletionFunc
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpForFlag --> fmt.Sprintf
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpForFlag --> strings.Join
```

**Raw-exact false negatives**:
```
  c-Users-user-Documents-bench-fixtures-cobra.bash_completions_test.TestBashCompletions --> c-Users-user-Documents-bench-fixtures-cobra.completions_test.String
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.genMan --> c-Users-user-Documents-bench-fixtures-cobra.command.CommandPath
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPreamble --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintFlags --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.command.InheritedFlags
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.command.NonInheritedFlags
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs_test.TestGenManDoc --> c-Users-user-Documents-bench-fixtures-cobra.command_test.checkStringContains
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs_test.TestGenManDoc --> c-Users-user-Documents-bench-fixtures-cobra.command_test.checkStringOmits
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs_test.TestGenManNoGenTag --> c-Users-user-Documents-bench-fixtures-cobra.command_test.checkStringOmits
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).