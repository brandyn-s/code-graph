#include "cbm.h"
#include "helpers.h"
#include "lang_specs.h"
#include "extract_unified.h"
#include <string.h>
#include <ctype.h>

// Deref-style wrapper types whose inner generic parameter is the
// "logical" type for method-dispatch purposes. `Arc<T>.method()` calls
// T::method via Deref; same for the rest. Includes Actix `web::Data<T>`,
// Rocket `State<T>`, and the common smart-pointer / interior-mutability
// crew. Excludes container types like `Vec<T>`, `HashMap<K,V>`, `Option<T>`,
// `Result<T,E>` whose method calls are on the container, not the inner.
//
// Order: longest first so `web::Data` is recognized before `Data`.
static const char* RUST_DEREF_WRAPPERS[] = {
    "web::Data", "actix_web::web::Data", "rocket::State",
    "Arc", "Rc", "Box", "Pin", "Cow",
    "Mutex", "RwLock", "RefCell", "Cell",
    "Lazy", "OnceCell", "OnceLock",
    NULL,
};

// True if `name` (already scope-stripped) matches any deref-style wrapper.
static bool is_deref_wrapper(const char* name) {
    if (!name) return false;
    for (const char** w = RUST_DEREF_WRAPPERS; *w; w++) {
        if (strcmp(name, *w) == 0) return true;
        // Also accept the leaf segment of a scoped wrapper.
        const char* slash = strrchr(*w, ':');
        if (slash && slash[1] && strcmp(name, slash + 1) == 0) return true;
    }
    return false;
}

// Strip leading `&`, `&mut `, and trailing `<...>` generics from a Rust type
// text in place. Peels deref-style wrappers (`Arc<T>` -> `T`, recursively).
// Examples:
//   `&BarType`               -> `BarType`
//   `&mut BarType`           -> `BarType`
//   `&'a BarType`            -> `BarType`  (lifetime annotation)
//   `Foo<T>`                 -> `Foo`         (non-wrapper: keep outer)
//   `Arc<MyType>`            -> `MyType`      (deref wrapper: peel)
//   `web::Data<AppState>`    -> `AppState`    (Actix wrapper)
//   `Arc<Mutex<MyType>>`     -> `MyType`      (recursive peel)
//   `Result<MyType, Error>`  -> `Result`      (non-wrapper: keep outer)
// Used for parameter / let-ascription / field type extraction so the bound
// type matches the canonical struct/trait/enum identifier in the registry.
static char* rust_type_text_normalize(char* text) {
    if (!text) return NULL;
    char* p = text;
    // Skip whitespace
    while (*p == ' ' || *p == '\t') p++;
    // Strip references: `&` (with optional mut/lifetime)
    if (*p == '&') {
        p++;
        while (*p == ' ' || *p == '\t') p++;
        // `'a ` lifetime
        if (*p == '\'') {
            p++;
            while (*p && !(*p == ' ' || *p == '\t')) p++;
            while (*p == ' ' || *p == '\t') p++;
        }
        // `mut`
        if (strncmp(p, "mut", 3) == 0 && (p[3] == ' ' || p[3] == '\t')) {
            p += 4;
            while (*p == ' ' || *p == '\t') p++;
        }
    }
    // Find outer type and any generic argument list. `Foo<bar>` -> name=Foo,
    // generic_inner=bar. For wrapper types we want to recurse on the inner.
    char* lt = strchr(p, '<');
    if (lt) {
        // Make a copy of the outer type so we can compare against the
        // wrapper list without mutating the caller's buffer for the inner
        // peel. We classify wrappers using the leaf identifier of the
        // outer (after `::` chain).
        char outer_buf[128];
        size_t outer_len = (size_t)(lt - p);
        // Trim trailing whitespace before `<`.
        while (outer_len > 0 && (p[outer_len - 1] == ' ' || p[outer_len - 1] == '\t')) {
            outer_len--;
        }
        if (outer_len < sizeof(outer_buf)) {
            memcpy(outer_buf, p, outer_len);
            outer_buf[outer_len] = '\0';
            // For wrapper detection, also check the scoped form.
            const char* leaf = strrchr(outer_buf, ':');
            leaf = leaf ? leaf + 1 : outer_buf;
            if (is_deref_wrapper(outer_buf) || is_deref_wrapper(leaf)) {
                // Recurse on the inner. Find the matching `>` (handle nesting).
                char* inner_start = lt + 1;
                char* inner_end = NULL;
                int depth = 1;
                for (char* q = inner_start; *q; q++) {
                    if (*q == '<') depth++;
                    else if (*q == '>') {
                        depth--;
                        if (depth == 0) { inner_end = q; break; }
                    }
                }
                if (inner_end) {
                    *inner_end = '\0';
                    // For multi-arg generics like Cow<'a, T>, take the last
                    // comma-separated argument (which is the actual data type
                    // in deref wrappers).
                    char* last_comma = strrchr(inner_start, ',');
                    char* recurse_on = last_comma ? last_comma + 1 : inner_start;
                    return rust_type_text_normalize(recurse_on);
                }
            }
        }
    }
    // Non-wrapper or no generics: scope-strip and generic-strip the outer.
    // For scoped paths (`a::b::Type`), keep only the trailing identifier.
    char* last_colon = strstr(p, "::");
    while (last_colon) {
        p = last_colon + 2;
        last_colon = strstr(p, "::");
    }
    // Strip trailing `<...>` generic suffix.
    char* gt = strchr(p, '<');
    if (gt) *gt = '\0';
    // Trim trailing whitespace
    char* end = p + strlen(p);
    while (end > p && (end[-1] == ' ' || end[-1] == '\t')) end--;
    *end = '\0';
    return p;
}

// Extract class/type name from a constructor expression.
// e.g., new Foo() -> "Foo", Foo() -> "Foo" (if uppercase), Foo{} -> "Foo"
static const char* extract_constructor_type(CBMArena* a, TSNode rhs, const char* source, CBMLanguage lang) {
    const char* kind = ts_node_type(rhs);

    // new_expression / object_creation_expression -> type field or first child
    if (strcmp(kind, "new_expression") == 0 || strcmp(kind, "object_creation_expression") == 0) {
        TSNode type_node = ts_node_child_by_field_name(rhs, "type", 4);
        if (!ts_node_is_null(type_node)) {
            const char* tk = ts_node_type(type_node);
            if (strcmp(tk, "type_identifier") == 0 || strcmp(tk, "identifier") == 0 ||
                strcmp(tk, "simple_identifier") == 0) {
                return cbm_node_text(a, type_node, source);
            }
            // generic_type: get base
            if (strcmp(tk, "generic_type") == 0 && ts_node_child_count(type_node) > 0) {
                return cbm_node_text(a, ts_node_child(type_node, 0), source);
            }
            return cbm_node_text(a, type_node, source);
        }
        // Fallback: first named child
        for (uint32_t i = 0; i < ts_node_child_count(rhs); i++) {
            TSNode child = ts_node_child(rhs, i);
            const char* ck = ts_node_type(child);
            if (strcmp(ck, "identifier") == 0 || strcmp(ck, "type_identifier") == 0 ||
                strcmp(ck, "simple_identifier") == 0) {
                return cbm_node_text(a, child, source);
            }
        }
    }

    // call_expression where function is an uppercase identifier (Python/Kotlin/Scala class construction)
    if (strcmp(kind, "call") == 0 || strcmp(kind, "call_expression") == 0) {
        TSNode func = ts_node_child_by_field_name(rhs, "function", 8);
        if (ts_node_is_null(func) && ts_node_child_count(rhs) > 0) {
            func = ts_node_child(rhs, 0);
        }
        if (!ts_node_is_null(func)) {
            char* fname = cbm_node_text(a, func, source);
            if (fname && fname[0] >= 'A' && fname[0] <= 'Z') {
                return fname;
            }
        }
    }

    // Go composite_literal: Type{...}
    if (strcmp(kind, "composite_literal") == 0) {
        TSNode type_node = ts_node_child_by_field_name(rhs, "type", 4);
        if (!ts_node_is_null(type_node)) {
            return cbm_node_text(a, type_node, source);
        }
    }

    // Rust: Type::new() or Type { ... }
    if (lang == CBM_LANG_RUST) {
        if (strcmp(kind, "struct_expression") == 0) {
            TSNode name = ts_node_child_by_field_name(rhs, "name", 4);
            if (!ts_node_is_null(name)) return cbm_node_text(a, name, source);
        }
    }

    return NULL;
}

// Walk AST for assignment patterns where RHS is a constructor call.
static void walk_type_assigns_body(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec);
static void walk_type_assigns(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec) {
    if (!cbm_walk_enter(ctx)) return;
    walk_type_assigns_body(ctx, node, spec);
    cbm_walk_leave(ctx);
}

static void walk_type_assigns_body(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec) {
    const char* kind = ts_node_type(node);

    // Assignment: var = Constructor()
    if (cbm_kind_in_set(node, spec->assignment_node_types)) {
        TSNode left = ts_node_child_by_field_name(node, "left", 4);
        TSNode right = ts_node_child_by_field_name(node, "right", 5);
        if (ts_node_is_null(right)) right = ts_node_child_by_field_name(node, "value", 5);
        if (!ts_node_is_null(left) && !ts_node_is_null(right)) {
            const char* lk = ts_node_type(left);
            if (strcmp(lk, "identifier") == 0 || strcmp(lk, "simple_identifier") == 0) {
                char* var_name = cbm_node_text(ctx->arena, left, ctx->source);
                const char* type_name = extract_constructor_type(ctx->arena, right, ctx->source, ctx->language);
                if (var_name && var_name[0] && type_name && type_name[0]) {
                    CBMTypeAssign ta;
                    ta.var_name = var_name;
                    ta.type_name = type_name;
                    ta.enclosing_func_qn = cbm_enclosing_func_qn_cached(ctx, node);
                    cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
                }
            }
        }
    }

    // Variable declarations with constructor RHS
    // Go: short_var_declaration, var_spec
    // JS/TS: variable_declarator (const x = new Foo())
    // Python: assignment already handled above
    // Rust: let_declaration
    if (strcmp(kind, "short_var_declaration") == 0 || strcmp(kind, "var_spec") == 0) {
        TSNode left = ts_node_child_by_field_name(node, "name", 4);
        if (ts_node_is_null(left)) left = ts_node_child_by_field_name(node, "left", 4);
        TSNode right = ts_node_child_by_field_name(node, "value", 5);
        if (ts_node_is_null(right)) right = ts_node_child_by_field_name(node, "right", 5);
        if (!ts_node_is_null(left) && !ts_node_is_null(right)) {
            char* var_name = cbm_node_text(ctx->arena, left, ctx->source);
            const char* type_name = extract_constructor_type(ctx->arena, right, ctx->source, ctx->language);
            if (var_name && var_name[0] && type_name && type_name[0]) {
                CBMTypeAssign ta;
                ta.var_name = var_name;
                ta.type_name = type_name;
                ta.enclosing_func_qn = cbm_enclosing_func_qn_cached(ctx, node);
                cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
            }
        }
    }

    if (strcmp(kind, "variable_declarator") == 0) {
        TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
        TSNode value_node = ts_node_child_by_field_name(node, "value", 5);
        if (!ts_node_is_null(name_node) && !ts_node_is_null(value_node)) {
            const char* nk = ts_node_type(name_node);
            if (strcmp(nk, "identifier") == 0 || strcmp(nk, "simple_identifier") == 0) {
                char* var_name = cbm_node_text(ctx->arena, name_node, ctx->source);
                const char* type_name = extract_constructor_type(ctx->arena, value_node, ctx->source, ctx->language);
                if (var_name && var_name[0] && type_name && type_name[0]) {
                    CBMTypeAssign ta;
                    ta.var_name = var_name;
                    ta.type_name = type_name;
                    ta.enclosing_func_qn = cbm_enclosing_func_qn_cached(ctx, node);
                    cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
                }
            }
        }
    }

    if (strcmp(kind, "let_declaration") == 0 && ctx->language == CBM_LANG_RUST) {
        TSNode pat = ts_node_child_by_field_name(node, "pattern", 7);
        TSNode val = ts_node_child_by_field_name(node, "value", 5);
        if (!ts_node_is_null(pat) && !ts_node_is_null(val)) {
            if (strcmp(ts_node_type(pat), "identifier") == 0) {
                char* var_name = cbm_node_text(ctx->arena, pat, ctx->source);
                const char* type_name = extract_constructor_type(ctx->arena, val, ctx->source, ctx->language);
                if (var_name && var_name[0] && type_name && type_name[0]) {
                    CBMTypeAssign ta;
                    ta.var_name = var_name;
                    ta.type_name = type_name;
                    ta.enclosing_func_qn = cbm_enclosing_func_qn_cached(ctx, node);
                    cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
                }
            }
        }
    }

    // Recurse
    uint32_t count = ts_node_child_count(node);
    for (uint32_t i = 0; i < count; i++) {
        walk_type_assigns(ctx, ts_node_child(node, i), spec);
    }
}

void cbm_extract_type_assigns(CBMExtractCtx* ctx) {
    const CBMLangSpec* spec = cbm_lang_spec(ctx->language);
    if (!spec) return;

    walk_type_assigns(ctx, ctx->root, spec);
}

// --- Unified handler ---

void handle_type_assigns(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec, WalkState* state) {
    const char* kind = ts_node_type(node);

    // Assignment: var = Constructor()
    if (spec->assignment_node_types && cbm_kind_in_set(node, spec->assignment_node_types)) {
        TSNode left = ts_node_child_by_field_name(node, "left", 4);
        TSNode right = ts_node_child_by_field_name(node, "right", 5);
        if (ts_node_is_null(right)) right = ts_node_child_by_field_name(node, "value", 5);
        if (!ts_node_is_null(left) && !ts_node_is_null(right)) {
            const char* lk = ts_node_type(left);
            if (strcmp(lk, "identifier") == 0 || strcmp(lk, "simple_identifier") == 0) {
                char* var_name = cbm_node_text(ctx->arena, left, ctx->source);
                const char* type_name = extract_constructor_type(ctx->arena, right,
                                                                  ctx->source, ctx->language);
                if (var_name && var_name[0] && type_name && type_name[0]) {
                    CBMTypeAssign ta;
                    ta.var_name = var_name;
                    ta.type_name = type_name;
                    ta.enclosing_func_qn = state->enclosing_func_qn;
                    cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
                }
            }
        }
    }

    // Go: short_var_declaration, var_spec
    if (strcmp(kind, "short_var_declaration") == 0 || strcmp(kind, "var_spec") == 0) {
        TSNode left = ts_node_child_by_field_name(node, "name", 4);
        if (ts_node_is_null(left)) left = ts_node_child_by_field_name(node, "left", 4);
        TSNode right = ts_node_child_by_field_name(node, "value", 5);
        if (ts_node_is_null(right)) right = ts_node_child_by_field_name(node, "right", 5);
        if (!ts_node_is_null(left) && !ts_node_is_null(right)) {
            char* var_name = cbm_node_text(ctx->arena, left, ctx->source);
            const char* type_name = extract_constructor_type(ctx->arena, right,
                                                              ctx->source, ctx->language);
            if (var_name && var_name[0] && type_name && type_name[0]) {
                CBMTypeAssign ta;
                ta.var_name = var_name;
                ta.type_name = type_name;
                ta.enclosing_func_qn = state->enclosing_func_qn;
                cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
            }
        }
    }

    // JS/TS: variable_declarator
    if (strcmp(kind, "variable_declarator") == 0) {
        TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
        TSNode value_node = ts_node_child_by_field_name(node, "value", 5);
        if (!ts_node_is_null(name_node) && !ts_node_is_null(value_node)) {
            const char* nk = ts_node_type(name_node);
            if (strcmp(nk, "identifier") == 0 || strcmp(nk, "simple_identifier") == 0) {
                char* var_name = cbm_node_text(ctx->arena, name_node, ctx->source);
                const char* type_name = extract_constructor_type(ctx->arena, value_node,
                                                                  ctx->source, ctx->language);
                if (var_name && var_name[0] && type_name && type_name[0]) {
                    CBMTypeAssign ta;
                    ta.var_name = var_name;
                    ta.type_name = type_name;
                    ta.enclosing_func_qn = state->enclosing_func_qn;
                    cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
                }
            }
        }
    }

    // Rust: let_declaration
    if (strcmp(kind, "let_declaration") == 0 && ctx->language == CBM_LANG_RUST) {
        TSNode pat = ts_node_child_by_field_name(node, "pattern", 7);
        TSNode val = ts_node_child_by_field_name(node, "value", 5);
        TSNode type_annot = ts_node_child_by_field_name(node, "type", 4);
        if (!ts_node_is_null(pat)) {
            if (strcmp(ts_node_type(pat), "identifier") == 0) {
                char* var_name = cbm_node_text(ctx->arena, pat, ctx->source);
                if (var_name && var_name[0]) {
                    const char* type_name = NULL;
                    // Prefer the explicit `let x: T = ...` annotation;
                    // fall back to constructor-RHS detection for
                    // `let x = T { ... }` / `let x = T::new(...)`.
                    if (!ts_node_is_null(type_annot)) {
                        char* tt = cbm_node_text(ctx->arena, type_annot, ctx->source);
                        type_name = rust_type_text_normalize(tt);
                    }
                    if ((!type_name || !type_name[0]) && !ts_node_is_null(val)) {
                        type_name = extract_constructor_type(ctx->arena, val,
                                                              ctx->source, ctx->language);
                    }
                    if (type_name && type_name[0]) {
                        CBMTypeAssign ta;
                        ta.var_name = var_name;
                        ta.type_name = type_name;
                        ta.enclosing_func_qn = state->enclosing_func_qn;
                        cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
                    }
                }
            }
        }
    }

    // Rust: function parameter — `fn foo(bar: BarType, ...)` binds bar->BarType.
    // 2026-05-02 plateau-diagnose Step 6 (wide sample): receiver-resolution
    // failure dominates 87% of the assetman residual. The dominant pattern is
    // `obj.method(...)` where `obj` is a function parameter whose type was
    // never bound in the TypeMap because CBM only emitted variable
    // assignments (`let x = Constructor()`), not parameter type ascriptions.
    if (strcmp(kind, "parameter") == 0 && ctx->language == CBM_LANG_RUST) {
        TSNode pat = ts_node_child_by_field_name(node, "pattern", 7);
        TSNode tnode = ts_node_child_by_field_name(node, "type", 4);
        if (!ts_node_is_null(pat) && !ts_node_is_null(tnode)) {
            const char* pk = ts_node_type(pat);
            if (strcmp(pk, "identifier") == 0) {
                char* var_name = cbm_node_text(ctx->arena, pat, ctx->source);
                char* type_text = cbm_node_text(ctx->arena, tnode, ctx->source);
                const char* type_name = rust_type_text_normalize(type_text);
                if (var_name && var_name[0] && type_name && type_name[0] &&
                    state->enclosing_func_qn) {
                    CBMTypeAssign ta;
                    ta.var_name = var_name;
                    ta.type_name = type_name;
                    ta.enclosing_func_qn = state->enclosing_func_qn;
                    cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
                }
            }
        }
    }

    // Rust: self_parameter — `&self`, `&mut self`, `self`. Inside an impl
    // block, `self` binds to the impl's type so that `self.field.method()`
    // chains can be type-resolved. enclosing_class_qn is the impl-scope QN.
    if (strcmp(kind, "self_parameter") == 0 && ctx->language == CBM_LANG_RUST) {
        if (state->enclosing_class_qn && state->enclosing_func_qn) {
            // The class QN may still carry generic suffixes from the impl
            // header (e.g. `TailscaleAuthService<S>`); strip them so the
            // binding matches the canonical struct definition's QN.
            char* cqn = (char*)state->enclosing_class_qn;
            char* lt = strchr(cqn, '<');
            char* normalized;
            if (lt) {
                size_t n = (size_t)(lt - cqn);
                normalized = cbm_arena_strndup(ctx->arena, cqn, n);
            } else {
                normalized = cqn;
            }
            // Strip any trailing whitespace from normalized.
            size_t nl = strlen(normalized);
            while (nl > 0 && (normalized[nl - 1] == ' ' || normalized[nl - 1] == '\t')) {
                normalized[nl - 1] = '\0';
                nl--;
            }
            if (normalized && normalized[0]) {
                // We bind self -> the *short* struct name, not the full QN.
                // The pipeline's resolveAsClass will canonicalize via the
                // registry, matching how other type names are resolved.
                const char* short_name = strrchr(normalized, '.');
                short_name = short_name ? short_name + 1 : normalized;
                CBMTypeAssign ta;
                ta.var_name = "self";
                ta.type_name = short_name;
                ta.enclosing_func_qn = state->enclosing_func_qn;
                cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
            }
        }
    }

    // Rust: function-scoped use_declaration — `use foo::bar::Baz;` inside
    // fn body brings `Baz` into scope. Emit a TypeAssign so the per-function
    // TypeMap can resolve `Baz.method()` calls. File-scoped uses are
    // handled by parse_rust_imports.
    //
    // Phase 3d (bench/research/registry-resolve-consolidation-plan.md):
    // targets the rust-diesel-negative `use schema::users::dsl::users;`
    // pattern where `users` is brought into scope inside `fn entry`.
    // Without this, `users.execute(conn)` had no receiver-type signal
    // and fell through to phantom-emitting suffix-match against
    // AssetRepo.execute.
    //
    // var_name and type_name are both set to the local (last) segment
    // so resolveAsClass can find an internal Class via byName lookup.
    // External crate types fall through to Phase 3c's raw recording and
    // get dropped by Tier 2.
    //
    // Skips use_list (`use foo::{a, b}`), glob (`use foo::*`), and `as`
    // rename forms — simple single-path uses only. More elaborate forms
    // can be added when the corpus surfaces them.
    if (strcmp(kind, "use_declaration") == 0 && ctx->language == CBM_LANG_RUST) {
        if (state->enclosing_func_qn) {
            char* full = cbm_node_text(ctx->arena, node, ctx->source);
            if (full) {
                char* p = full;
                if (strncmp(p, "use ", 4) == 0) p += 4;
                size_t len = strlen(p);
                while (len > 0 && (p[len - 1] == ';' || p[len - 1] == ' ' ||
                                   p[len - 1] == '\t' || p[len - 1] == '\n')) {
                    p[--len] = '\0';
                }
                if (p[0] && !strchr(p, '{') && !strchr(p, '*') && !strstr(p, " as ")) {
                    const char* local = strrchr(p, ':');
                    local = local ? local + 1 : p;
                    if (local[0]) {
                        char* local_copy = cbm_arena_strdup(ctx->arena, local);
                        CBMTypeAssign ta;
                        ta.var_name = local_copy;
                        ta.type_name = local_copy;
                        ta.enclosing_func_qn = state->enclosing_func_qn;
                        cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
                    }
                }
            }
        }
    }

    // Rust: field_declaration inside struct/enum/union — bind field name to
    // its declared type, scoped to the enclosing struct's QN. Emitted with
    // `enclosing_func_qn = struct_QN` (i.e. enclosing CLASS scope) so the
    // pipeline can distinguish field bindings from local-variable bindings
    // by checking the label on enclosing_func_qn (Class/Struct vs Function).
    if (strcmp(kind, "field_declaration") == 0 && ctx->language == CBM_LANG_RUST) {
        if (state->enclosing_class_qn) {
            TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
            TSNode type_node = ts_node_child_by_field_name(node, "type", 4);
            if (!ts_node_is_null(name_node) && !ts_node_is_null(type_node)) {
                char* var_name = cbm_node_text(ctx->arena, name_node, ctx->source);
                char* type_text = cbm_node_text(ctx->arena, type_node, ctx->source);
                const char* type_name = rust_type_text_normalize(type_text);
                if (var_name && var_name[0] && type_name && type_name[0]) {
                    // Strip generic suffix from class scope QN to match
                    // canonical struct QN in registry.
                    char* cqn = (char*)state->enclosing_class_qn;
                    char* lt = strchr(cqn, '<');
                    const char* class_qn;
                    if (lt) {
                        size_t n = (size_t)(lt - cqn);
                        char* trimmed = cbm_arena_strndup(ctx->arena, cqn, n);
                        // Trim trailing whitespace
                        size_t tl = strlen(trimmed);
                        while (tl > 0 && (trimmed[tl - 1] == ' ' || trimmed[tl - 1] == '\t')) {
                            trimmed[tl - 1] = '\0';
                            tl--;
                        }
                        class_qn = trimmed;
                    } else {
                        class_qn = cqn;
                    }
                    CBMTypeAssign ta;
                    ta.var_name = var_name;
                    ta.type_name = type_name;
                    ta.enclosing_func_qn = class_qn;
                    cbm_typeassign_push(&ctx->result->type_assigns, ctx->arena, ta);
                }
            }
        }
    }
}
