//! CALLS+IMPORTS ground-truth oracle for Rust fixtures via `syn`.
//!
//! Two modes:
//!   1. Crate walk (oracle mode):
//!        `oracle-rust-syn <crate_root> <project_name>`
//!      Walks every .rs file under <crate_root>, emits edges + def QNs.
//!      This is the original ground-truth-oracle invocation used by
//!      `bench/accuracy/oracle_rust_syn.py`.
//!   2. Single-file (production-extractor mode, Phase A'''' Option α
//!      Session 1):
//!        `oracle-rust-syn --single-file <abs_path> --crate-root <abs_path> --project <name>`
//!      Parses a single .rs file at <abs_path>, computes its module_qn
//!      relative to <crate_root>, emits the same edge shape but with
//!      file-resolved `line` numbers (proc-macro2 spans) instead of 0.
//!      This is the foundational building block for the C-extractor
//!      Rust-path replacement named in PR #330 (Phase A''' Session 2's
//!      named pivot — closures / async / macros / struct-literal /
//!      turbofish coverage that the C extractor misses).
//!
//! Emits JSON array of edges to stdout, shape:
//!   [{"from_qn": "...", "to_qn": "...", "type": "CALLS"|"IMPORTS",
//!     "file": "...", "line": N, "source": "syn"}]
//!
//! Design decisions:
//! - `syn` parses unexpanded source (same as code-graph's tree-sitter).
//!   Macro-generated calls are invisible on both sides -> apples-to-apples.
//! - QN format matches code-graph's `fqn.Compute`: `<project>.<rel_path>.<name>`
//!   with `::` path segments converted to `.` for callee QNs.
//! - Caller QN includes nested function hierarchy (impls, nested fns, closures
//!   are NOT named -> we use the nearest named enclosing fn).
//! - Callee QN is the syntactic form as written at the call site. The Python
//!   wrapper does internal-vs-external filtering (same pattern as oracle_pycg.py).
//! - Line numbers (Session 1, 2026-05-23): proc-macro2 `Span::start()` exposes
//!   `LineColumn { line, column }` when the `span-locations` feature is on.
//!   The crate-walk mode also emits real line numbers now — the original
//!   `line: 0` comment was inherited from before this feature was enabled
//!   and is no longer accurate. Oracle consumers that don't read `line`
//!   are unaffected; consumers that do read it now get useful values.

use std::env;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::ExitCode;

use proc_macro2::Span;
use serde::Serialize;
use syn::spanned::Spanned;
use syn::visit::Visit;
use syn::{Expr, ExprCall, ExprMethodCall, ExprPath, ImplItemFn, ItemFn, ItemMod, ItemUse, UseTree};
use walkdir::WalkDir;

#[derive(Serialize, Clone)]
struct Edge {
    from_qn: String,
    to_qn: String,
    #[serde(rename = "type")]
    edge_type: String,
    file: String,
    line: u32,
    source: String,
}

struct Visitor {
    /// `<project>.<rel_path_dotted>`, e.g. `apid.src.main`.
    module_qn: String,
    /// Relative to crate root, for edge.file.
    file_rel: String,
    /// Stack of enclosing inline `mod X { ... }` names. Inline mods are part
    /// of the caller QN: `mod helpers { fn greet() {} }` -> caller =
    /// `<module_qn>.helpers.greet`. File-level mods (`mod foo;`) don't come
    /// in via this — those are separate .rs files the walker already visits.
    mod_stack: Vec<String>,
    /// Stack of enclosing named fn segments. For `impl Foo { fn bar() }` we
    /// push `Foo.bar`; for free `fn baz()` we push `baz`.
    fn_stack: Vec<String>,
    /// Currently inside `impl T` (or `impl Trait for T`) — used to qualify
    /// method names with the type.
    impl_stack: Vec<String>,
    edges: Vec<Edge>,
    /// Full QNs for every fn definition we encounter. The Python wrapper
    /// uses these to resolve bare-name calls to full QNs that match
    /// code-graph's node QNs (including impl/mod scope).
    defs: Vec<String>,
}

impl Visitor {
    fn current_caller(&self) -> String {
        let mut parts = vec![self.module_qn.clone()];
        parts.extend(self.mod_stack.iter().cloned());
        parts.extend(self.fn_stack.iter().cloned());
        parts.join(".")
    }
}

impl<'ast> Visit<'ast> for Visitor {
    fn visit_item_fn(&mut self, i: &'ast ItemFn) {
        self.fn_stack.push(i.sig.ident.to_string());
        // Record def QN with full impl/mod scope. The Python wrapper resolves
        // bare-name calls against this set.
        self.defs.push(self.current_caller());
        syn::visit::visit_item_fn(self, i);
        self.fn_stack.pop();
    }

    fn visit_impl_item_fn(&mut self, i: &'ast ImplItemFn) {
        // Methods inside an impl block: push "<Type>.<method>".
        let ty = self.impl_stack.last().cloned().unwrap_or_default();
        let name = i.sig.ident.to_string();
        let segment = if ty.is_empty() { name.clone() } else { format!("{}.{}", ty, name) };
        self.fn_stack.push(segment);
        self.defs.push(self.current_caller());
        syn::visit::visit_impl_item_fn(self, i);
        self.fn_stack.pop();
    }

    fn visit_item_impl(&mut self, i: &'ast syn::ItemImpl) {
        // Extract the self type name for method qualification.
        // `impl Foo` -> "Foo", `impl Trait for Foo` -> "Foo".
        //
        // 2026-05-02: a brief experiment (THEME D, PR #144) pushed the
        // *trait* name on trait impls instead of self_ty. Measured
        // +3.3pp F1 against stale per-subset indexes (predating the
        // Janusian penalty in #135). Re-validation against fresh
        // indexes showed the change was a -2.9pp F1 *regression*
        // (0.820 → 0.791): Janusian penalty already drops trait/impl
        // mismatch FPs, leaving only ~3 for THEME D to fix, while the
        // recall slip (-32 TPs) from oracle/CG-form disagreement
        // remained. Reverted to self_ty. See
        // baselines/2026-05-02-rust-theme-d-revert.md for full
        // attribution and why the stale-index discipline check that
        // ships alongside this revert would have caught the surprise
        // earlier.
        let ty_name = match &*i.self_ty {
            syn::Type::Path(tp) => tp.path.segments.last().map(|s| s.ident.to_string()).unwrap_or_default(),
            _ => String::new(),
        };
        self.impl_stack.push(ty_name);
        syn::visit::visit_item_impl(self, i);
        self.impl_stack.pop();
    }

    fn visit_item_mod(&mut self, i: &'ast ItemMod) {
        // Descend into `mod X { ... }` (inline) WITHOUT pushing the mod name
        // onto the caller-stack. Empirically (2026-04-24 verification),
        // code-graph's `fqn.Compute` does not track nested-mod scope — a fn
        // defined in `mod tests { fn foo() }` is stored as `<file>.foo`, not
        // `<file>.tests.foo`. If we push mod names, the oracle's QNs diverge
        // from code-graph's for inline-mod callers (was 88 FNs / 38.8% of
        // total recall gap).
        //
        // Mod scope is still NEEDED for impl resolution: `mod x { impl T { fn
        // foo() } }` — but since the impl's type name is already on impl_stack,
        // we don't need the mod.
        if i.content.is_some() {
            syn::visit::visit_item_mod(self, i);
        }
    }

    fn visit_item_use(&mut self, i: &'ast ItemUse) {
        let mut prefix = Vec::<String>::new();
        let use_line = line_of(i.span());
        emit_use_tree(&i.tree, &mut prefix, &mut self.edges, &self.module_qn, &self.file_rel, use_line);
    }

    fn visit_expr_call(&mut self, i: &'ast ExprCall) {
        // func() where func is typically a Path like `foo::bar` or a plain ident.
        if let Some(callee) = path_from_expr(&i.func) {
            self.edges.push(Edge {
                from_qn: self.current_caller(),
                to_qn: callee,
                edge_type: "CALLS".to_string(),
                file: self.file_rel.clone(),
                line: line_of(i.span()),
                source: "syn".to_string(),
            });
        }
        syn::visit::visit_expr_call(self, i);
    }

    fn visit_expr_method_call(&mut self, i: &'ast ExprMethodCall) {
        // receiver.method(args). We can only name the method syntactically;
        // receiver type is unresolved (same limit code-graph tree-sitter has).
        // Emit with bare method name as to_qn — the Python wrapper filters
        // by ambiguity (only emit when the bare name has exactly one def
        // in the project, regardless of whether ExprCall or ExprMethodCall).
        let callee = i.method.to_string();
        self.edges.push(Edge {
            from_qn: self.current_caller(),
            to_qn: callee,
            edge_type: "CALLS".to_string(),
            file: self.file_rel.clone(),
            line: line_of(i.method.span()),
            source: "syn".to_string(),
        });
        syn::visit::visit_expr_method_call(self, i);
    }
}

/// Extract a 1-based line number from a proc-macro2 Span. Requires the
/// `span-locations` feature on proc-macro2 (enabled in Cargo.toml).
/// Returns 0 on the no-span case (shouldn't happen for parsed source).
fn line_of(span: Span) -> u32 {
    let start = span.start();
    u32::try_from(start.line).unwrap_or(0)
}

/// Best-effort path extraction from a callee expression. `foo::bar` -> "foo.bar",
/// `foo` -> "foo", `(expr)` -> None, `closure_var(...)` -> None.
fn path_from_expr(e: &Expr) -> Option<String> {
    match e {
        Expr::Path(ExprPath { path, .. }) => {
            let segs: Vec<String> = path.segments.iter().map(|s| s.ident.to_string()).collect();
            Some(segs.join("."))
        }
        Expr::Paren(p) => path_from_expr(&p.expr),
        _ => None,
    }
}

/// Walk a UseTree recursively, emitting IMPORTS edges for every leaf module path.
/// `use foo::bar::{baz, qux};` emits two edges: foo.bar.baz and foo.bar.qux.
/// `use foo::bar::baz as b;` emits one edge to foo.bar.baz.
fn emit_use_tree(
    tree: &UseTree,
    prefix: &mut Vec<String>,
    edges: &mut Vec<Edge>,
    module_qn: &str,
    file_rel: &str,
    use_line: u32,
) {
    match tree {
        UseTree::Path(p) => {
            prefix.push(p.ident.to_string());
            emit_use_tree(&p.tree, prefix, edges, module_qn, file_rel, use_line);
            prefix.pop();
        }
        UseTree::Name(n) => {
            let mut parts = prefix.clone();
            parts.push(n.ident.to_string());
            edges.push(Edge {
                from_qn: module_qn.to_string(),
                to_qn: parts.join("."),
                edge_type: "IMPORTS".to_string(),
                file: file_rel.to_string(),
                line: use_line,
                source: "syn".to_string(),
            });
        }
        UseTree::Rename(r) => {
            let mut parts = prefix.clone();
            parts.push(r.ident.to_string());
            edges.push(Edge {
                from_qn: module_qn.to_string(),
                to_qn: parts.join("."),
                edge_type: "IMPORTS".to_string(),
                file: file_rel.to_string(),
                line: use_line,
                source: "syn".to_string(),
            });
        }
        UseTree::Glob(_) => {
            // `use foo::bar::*;` — emit edge to the parent module. That's the
            // best we can do syntactically; individual symbols are invisible.
            if !prefix.is_empty() {
                edges.push(Edge {
                    from_qn: module_qn.to_string(),
                    to_qn: prefix.join("."),
                    edge_type: "IMPORTS".to_string(),
                    file: file_rel.to_string(),
                    line: use_line,
                    source: "syn".to_string(),
                });
            }
        }
        UseTree::Group(g) => {
            for item in &g.items {
                emit_use_tree(item, prefix, edges, module_qn, file_rel, use_line);
            }
        }
    }
}

/// Compute `<project>.<rel_path_without_ext>` with path seps converted to dots.
/// `src/main.rs` + project `apid` -> `apid.src.main`.
fn module_qn(project: &str, rel_path: &Path) -> String {
    let rel_str = rel_path.with_extension("").to_string_lossy().replace('\\', "/");
    let parts: Vec<&str> = rel_str.split('/').filter(|s| !s.is_empty()).collect();
    let mut all = vec![project.to_string()];
    all.extend(parts.iter().map(|s| s.to_string()));
    all.join(".")
}

fn process_file(
    path: &Path,
    crate_root: &Path,
    project: &str,
    all_edges: &mut Vec<Edge>,
    all_defs: &mut Vec<String>,
) -> Result<(), String> {
    let source = fs::read_to_string(path).map_err(|e| format!("read {}: {}", path.display(), e))?;
    let syntax = match syn::parse_file(&source) {
        Ok(f) => f,
        Err(e) => return Err(format!("parse {}: {}", path.display(), e)),
    };
    let rel = path.strip_prefix(crate_root).map_err(|_| "strip_prefix failed".to_string())?;
    let rel_str = rel.to_string_lossy().replace('\\', "/");
    let mqn = module_qn(project, rel);
    let mut v = Visitor {
        module_qn: mqn,
        file_rel: rel_str,
        mod_stack: Vec::new(),
        fn_stack: Vec::new(),
        impl_stack: Vec::new(),
        edges: Vec::new(),
        defs: Vec::new(),
    };
    v.visit_file(&syntax);
    all_edges.extend(v.edges);
    all_defs.extend(v.defs);
    Ok(())
}

fn print_usage() {
    eprintln!(
        "usage:\n  \
         oracle-rust-syn <crate_root> <project_name>\n  \
         oracle-rust-syn --single-file <abs_path> --crate-root <abs_path> --project <name>"
    );
}

fn main() -> ExitCode {
    let args: Vec<String> = env::args().collect();

    // Mode 2: --single-file <abs_path> --crate-root <abs_path> --project <name>
    // (flag order tolerant)
    if args.iter().any(|a| a == "--single-file") {
        let mut single_file: Option<PathBuf> = None;
        let mut crate_root_arg: Option<PathBuf> = None;
        let mut project_arg: Option<String> = None;

        let mut i = 1;
        while i < args.len() {
            match args[i].as_str() {
                "--single-file" => {
                    i += 1;
                    if i >= args.len() {
                        eprintln!("--single-file requires a path argument");
                        print_usage();
                        return ExitCode::from(2);
                    }
                    single_file = Some(PathBuf::from(&args[i]));
                }
                "--crate-root" => {
                    i += 1;
                    if i >= args.len() {
                        eprintln!("--crate-root requires a path argument");
                        print_usage();
                        return ExitCode::from(2);
                    }
                    crate_root_arg = Some(PathBuf::from(&args[i]));
                }
                "--project" => {
                    i += 1;
                    if i >= args.len() {
                        eprintln!("--project requires a name argument");
                        print_usage();
                        return ExitCode::from(2);
                    }
                    project_arg = Some(args[i].clone());
                }
                other => {
                    eprintln!("unrecognized argument: {}", other);
                    print_usage();
                    return ExitCode::from(2);
                }
            }
            i += 1;
        }

        let single_file = match single_file {
            Some(p) => p,
            None => {
                eprintln!("--single-file mode requires a --single-file <path> argument");
                print_usage();
                return ExitCode::from(2);
            }
        };
        let crate_root = match crate_root_arg {
            Some(p) => p,
            None => {
                eprintln!("--single-file mode requires --crate-root <path>");
                print_usage();
                return ExitCode::from(2);
            }
        };
        let project = match project_arg {
            Some(p) => p,
            None => {
                eprintln!("--single-file mode requires --project <name>");
                print_usage();
                return ExitCode::from(2);
            }
        };

        if !single_file.is_file() {
            eprintln!("--single-file path is not a regular file: {}", single_file.display());
            return ExitCode::from(2);
        }
        if !crate_root.is_dir() {
            eprintln!("--crate-root is not a directory: {}", crate_root.display());
            return ExitCode::from(2);
        }
        if !single_file.starts_with(&crate_root) {
            eprintln!(
                "--single-file ({}) is not under --crate-root ({})",
                single_file.display(),
                crate_root.display()
            );
            return ExitCode::from(2);
        }

        let mut edges: Vec<Edge> = Vec::new();
        let mut defs: Vec<String> = Vec::new();
        match process_file(&single_file, &crate_root, &project, &mut edges, &mut defs) {
            Ok(()) => {
                eprintln!(
                    "oracle-rust-syn --single-file: project={} file={} edges={} defs={}",
                    project,
                    single_file.display(),
                    edges.len(),
                    defs.len()
                );
                #[derive(Serialize)]
                struct Output<'a> {
                    edges: &'a [Edge],
                    defs: &'a [String],
                }
                let out = Output { edges: &edges, defs: &defs };
                let json = serde_json::to_string(&out).expect("serde_json");
                println!("{}", json);
                ExitCode::SUCCESS
            }
            Err(e) => {
                eprintln!("oracle-rust-syn --single-file: parse error: {}", e);
                ExitCode::from(1)
            }
        }
    } else {
        // Mode 1: original crate-walk oracle invocation.
        if args.len() != 3 {
            print_usage();
            return ExitCode::from(2);
        }
        let crate_root = PathBuf::from(&args[1]);
        let project = &args[2];

        if !crate_root.is_dir() {
            eprintln!("crate_root not a directory: {}", crate_root.display());
            return ExitCode::from(2);
        }

        let mut edges: Vec<Edge> = Vec::new();
        let mut defs: Vec<String> = Vec::new();
        let mut errors: Vec<String> = Vec::new();
        let mut parsed = 0usize;

        for entry in WalkDir::new(&crate_root).into_iter().filter_map(|e| e.ok()) {
            let p = entry.path();
            if !p.is_file() {
                continue;
            }
            if p.extension().and_then(|s| s.to_str()) != Some("rs") {
                continue;
            }
            // Skip target/ dirs, tests under targets, build.rs outputs.
            let s = p.to_string_lossy().replace('\\', "/");
            if s.contains("/target/") || s.ends_with("/build.rs") {
                continue;
            }
            match process_file(p, &crate_root, project, &mut edges, &mut defs) {
                Ok(()) => parsed += 1,
                Err(e) => errors.push(e),
            }
        }

        eprintln!(
            "oracle-rust-syn: project={} files_parsed={} edges={} defs={} errors={}",
            project,
            parsed,
            edges.len(),
            defs.len(),
            errors.len()
        );
        for e in &errors {
            eprintln!("  error: {}", e);
        }

        // Emit both edges and def QNs. Python wrapper uses defs to resolve bare
        // calls without re-parsing the crate.
        #[derive(Serialize)]
        struct Output<'a> {
            edges: &'a [Edge],
            defs: &'a [String],
        }
        let out = Output { edges: &edges, defs: &defs };
        let json = serde_json::to_string(&out).expect("serde_json");
        println!("{}", json);
        ExitCode::SUCCESS
    }
}
