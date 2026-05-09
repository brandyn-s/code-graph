# code-graph accuracy baseline — cobra-go

- **Date**: 2026-05-09
- **Fixture SHA**: `f2878bab8c96afd6e36968af96343b35dbb82a82` (short: `f2878ba`)
- **Project name**: `c-Users-user-Documents-bench-fixtures-cobra`

## Summary

Five metrics per edge type:
- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.
- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.
- **Scope-aligned (all bands)**: restricted to edges whose caller is in the oracle's analyzed-caller set, INCLUDING speculative-janusian band emissions (Janusian-ambiguous cross-package edges). Reflects the recall-friendly operating point — what consumers see by default if they don't filter `confidence_band`.
- **Scope-aligned (high-confidence)**: same scope, EXCLUDING `confidence_band=speculative-janusian` edges. Reflects the precision-friendly operating point for consumers who filter to high-trust edges only.
- **Impl-normalized**: Rust-specific. Strips `Impl` suffix from penultimate QN segment symmetrically on both sides — treats `FooImpl.bar` and `Foo.bar` as the same function. Captures code-graph's trait-form vs oracle's impl-form resolution disagreement.

Two operating points are reported because the resolver emits Janusian-ambiguous edges (multiple cross-package candidates with same simple name) at the speculative-janusian band rather than dropping them. Find-one-function-reliably queries should filter to high-confidence; blast-radius / impact-analysis queries should use all-bands.

| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Scope-aligned P/R/F1 (all) | Scope-aligned P/R/F1 (high-conf) | Impl-normalized P/R/F1 |
|---|---|---|---|---|---|---|
| CALLS | go-ast | 906 / 1585 | 0.564 / 0.987 / 0.718 | 0.895 / 0.987 / 0.939 | 0.895 / 0.987 / 0.939 | 0.895 / 0.987 / 0.939 |
| IMPORTS | go-ast (dropped) | 0 / 0 | — (Go oracle drops IMPORTS until import-path -> internal-file-Q) | — | — |

## Per-project scope-aligned F1

Aggregate F1 can hide variance across subsets. If the headline scope-aligned F1 is a mean of widely-varying per-subset numbers, investigate the low outliers separately.

### CALLS

| Project | Oracle / Measured | TP | FP | FN | P | R | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| cobra | 906 / 1585 | 894 | 564 | 12 | 0.613 | 0.987 | **0.756** |

**Spread**: min F1 = 0.756, max F1 = 0.756, range = 0.000


## Caller-kind stratified precision

Each CALLS edge is tagged with the AST scope of its caller (`function-body`, `method-body`, `file-block`, `package-init-block`, `var-init`, `type-decl`, `test-body`, `closure`, `unknown`). The harness reads this property and stratifies precision by it. The **ghost-caller FP rate** is the share of FPs whose caller is a package-level scope rather than a real function/method — alarms above 5%.

### CALLS

| Kind | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `function-body` | 145 | 46 | 0.759 | 191 |
| `method-body` | 287 | 59 | 0.830 | 346 |
| `test-body` | 462 | 0 | 1.000 | 462 |

**Package-block caller FP rate**: 0.0000 (0 of 105 FPs)

**Caller-kind complement legitimacy** (function/method-body share of all scope-aligned edges): 0.5312 (537 of 1011)


## Janusian ambiguity stratified precision

Each CALLS edge carries the resolver's pre-tie-break candidate cardinality (`candidate_set_size`). A call site with >= 2 candidates is **Janusian** — the resolver picked among alternatives. Step 2's LLM-Judge taxonomy predicted `same_named_method_disambiguation` (60% of judged FPs) concentrates on Janusian sites; the precision split below tests that hypothesis on real-fixture data. LSP-resolved edges carry size=1 by definition (LSP returns one target without enumerating alternates), so the Janusian signal lives in the registry strategies.

### CALLS

**method_set_ambiguity_index** — share of call sites with >= 2 candidates:

| Project | Ambiguous sites | Total sites | Index |
|---|---:|---:|---:|
| cobra | 2 | 439 | 0.0046 |

**janusian_site_precision_split** — precision conditional on call-site ambiguity:

| Bucket | TP | FP | Precision | Support |
|---|---:|---:|---:|---:|
| `ambiguous` | 0 | 2 | 0.0000 | 2 |
| `unambiguous` | 894 | 103 | 0.8967 | 997 |

**janusian_precision_gap** (unambiguous − ambiguous precision): +0.8967. Positive = unambiguous sites resolve more accurately, consistent with Step 2's prediction. Negative or near-zero = ambiguity is not the dominant FP driver.


## Samples (first 10 per edge type)

### CALLS

Oracle analyzed callers: 442

**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):
```
  c-Users-user-Documents-bench-fixtures-cobra.bash_completions.gen --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.IsAvailableCommand
  c-Users-user-Documents-bench-fixtures-cobra.bash_completions.writeCommands --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.IsAvailableCommand
  c-Users-user-Documents-bench-fixtures-cobra.bash_completions.writeCommands --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.Name
  c-Users-user-Documents-bench-fixtures-cobra.command.Command.DebugFlags --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.HasFlags
  c-Users-user-Documents-bench-fixtures-cobra.command.Command.DebugFlags --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.HasPersistentFlags
  c-Users-user-Documents-bench-fixtures-cobra.command.Command.DebugFlags --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.HasSubCommands
  c-Users-user-Documents-bench-fixtures-cobra.command.Command.DebugFlags --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.persistentFlag
  c-Users-user-Documents-bench-fixtures-cobra.command.Command.ExecuteC --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.HelpFunc
  c-Users-user-Documents-bench-fixtures-cobra.command.Command.ExecuteC --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.Name
  c-Users-user-Documents-bench-fixtures-cobra.command.Command.ExecuteC --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.UsageString
```

**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):
```
  c-Users-user-Documents-bench-fixtures-cobra.bash_completions_test.TestBashCompletions --> c-Users-user-Documents-bench-fixtures-cobra.completions_test.customMultiString.String
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.genMan --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.CommandPath
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPreamble --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintFlags --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.InheritedFlags
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.NonInheritedFlags
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_examples_test.ExampleGenMan --> c-Users-user-Documents-bench-fixtures-cobra.cobra.CheckErr
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_examples_test.ExampleGenMan --> c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.GenMan
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_examples_test.ExampleGenManTree --> c-Users-user-Documents-bench-fixtures-cobra.cobra.CheckErr
```

**Raw-exact false positives (may include out-of-scope callers)**:
```
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpAlone --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.AddCommand
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpForFlag --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.Flags
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpForFlag --> c-Users-user-Documents-bench-fixtures-cobra.completions.Command.RegisterFlagCompletionFunc
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestActiveHelpWithComps --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.AddCommand
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestConfigActiveHelp --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.AddCommand
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestConfigActiveHelp --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.Flags
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestConfigActiveHelp --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.Name
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestConfigActiveHelp --> c-Users-user-Documents-bench-fixtures-cobra.completions.Command.RegisterFlagCompletionFunc
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestDisableActiveHelp --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.AddCommand
  c-Users-user-Documents-bench-fixtures-cobra.active_help_test.TestDisableActiveHelp --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.Name
```

**Raw-exact false negatives**:
```
  c-Users-user-Documents-bench-fixtures-cobra.bash_completions_test.TestBashCompletions --> c-Users-user-Documents-bench-fixtures-cobra.completions_test.customMultiString.String
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.genMan --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.CommandPath
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPreamble --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintFlags --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.cobra.WriteStringAndCheck
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.InheritedFlags
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.manPrintOptions --> c-Users-user-Documents-bench-fixtures-cobra.command.Command.NonInheritedFlags
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_examples_test.ExampleGenMan --> c-Users-user-Documents-bench-fixtures-cobra.cobra.CheckErr
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_examples_test.ExampleGenMan --> c-Users-user-Documents-bench-fixtures-cobra.doc.man_docs.GenMan
  c-Users-user-Documents-bench-fixtures-cobra.doc.man_examples_test.ExampleGenManTree --> c-Users-user-Documents-bench-fixtures-cobra.cobra.CheckErr
```

## Targets

- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).
- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).
- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).