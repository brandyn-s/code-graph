#include "cbm.h"
#include "helpers.h"
#include "lang_specs.h"
#include "extract_unified.h"
#include <string.h>
#include <ctype.h>

// Forward declarations
static void walk_calls(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec);
static char* extract_callee_name(CBMArena* a, TSNode node, const char* source, CBMLanguage lang);
static void extract_jsx_refs(CBMExtractCtx* ctx, TSNode node);

// cbm_python_flask_hook_label classifies a Python call expression's
// dotted callee_name against the Flask hook-registrar allowlist from
// INDIRECT_CALLS v0.3 Pattern A. Returns the per-registrar
// dispatch_kind label string when the callee ends in one of the
// seven registered suffixes; NULL otherwise.
//
// The seven registrars all share the same emission shape: at the
// call site (which must be inside an enclosing function, otherwise
// the resulting edge would land in CALLS_PSEUDO and be invisible to
// trace_call_path), the first identifier argument names the
// function being registered as a Flask hook. We emit that function
// as a synthesized callee with dispatch_kind tagged to the
// registrar's family.
//
// Allowlist sourced from internal/pipeline/INDIRECT_CALLS_V0_3_PLAN.md.
// Mirrors the existing executor.submit / Depends / getattr Call-
// synthesis pattern (see emission blocks below).
static const char* cbm_python_flask_hook_label(const char* callee) {
    if (!callee) return NULL;
    size_t n = strlen(callee);
    static const struct {
        const char* suffix;
        size_t      len;
        const char* label;
    } rules[] = {
        { ".before_request",       15, "before_request_hook"       },
        { ".after_request",        14, "after_request_hook"        },
        { ".teardown_request",     17, "teardown_request_hook"     },
        { ".teardown_appcontext",  20, "teardown_appcontext_hook"  },
        { ".errorhandler",         13, "errorhandler_hook"         },
        { ".context_processor",    18, "context_processor_hook"    },
        { ".before_first_request", 21, "before_first_request_hook" },
    };
    for (size_t i = 0; i < sizeof(rules) / sizeof(rules[0]); i++) {
        if (n >= rules[i].len &&
            strcmp(callee + n - rules[i].len, rules[i].suffix) == 0) {
            return rules[i].label;
        }
    }
    return NULL;
}

// Extract callee name from a call node
static char* extract_callee_name(CBMArena* a, TSNode node, const char* source, CBMLanguage lang) {
    // PowerShell: `command` carries the callee in a `command_name` FIELD
    // (bash's equivalent field is `name`, so the generic probes miss it);
    // `invokation_expression` method calls name the member via a
    // `member_name`-typed child.
    if (lang == CBM_LANG_POWERSHELL) {
        const char* k = ts_node_type(node);
        if (strcmp(k, "command") == 0) {
            TSNode cn = ts_node_child_by_field_name(node, "command_name", 12);
            if (!ts_node_is_null(cn)) return cbm_node_text(a, cn, source);
        }
        if (strcmp(k, "invokation_expression") == 0) {
            TSNode mn = cbm_find_child_by_kind(node, "member_name");
            if (!ts_node_is_null(mn)) return cbm_node_text(a, mn, source);
        }
    }

    // Try "function" field (most languages: call_expression, etc.)
    TSNode func_node = ts_node_child_by_field_name(node, "function", 8);
    // Rust ACC-002: turbofish wraps the function in `generic_function` —
    // unwrap to its inner `function` child so kind-matching below sees the
    // real callee shape (identifier / scoped_identifier / field_expression).
    if (!ts_node_is_null(func_node) && lang == CBM_LANG_RUST &&
        strcmp(ts_node_type(func_node), "generic_function") == 0) {
        TSNode inner = ts_node_child_by_field_name(func_node, "function", 8);
        if (!ts_node_is_null(inner)) {
            func_node = inner;
        }
    }
    if (!ts_node_is_null(func_node)) {
        const char* fk = ts_node_type(func_node);
        if (strcmp(fk, "identifier") == 0 ||
            strcmp(fk, "simple_identifier") == 0 ||
            strcmp(fk, "selector_expression") == 0 ||
            strcmp(fk, "attribute") == 0 ||
            strcmp(fk, "member_expression") == 0 ||
            strcmp(fk, "field_expression") == 0 ||
            strcmp(fk, "dot") == 0 ||
            strcmp(fk, "function") == 0 ||
            strcmp(fk, "dotted_identifier") == 0 ||
            strcmp(fk, "member_access_expression") == 0 ||
            strcmp(fk, "scoped_identifier") == 0 ||
            strcmp(fk, "qualified_identifier") == 0) {
            return cbm_node_text(a, func_node, source);
        }
    }

    // Try "name" field (Java method_invocation)
    TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
    if (!ts_node_is_null(name_node)) {
        char* name = cbm_node_text(a, name_node, source);
        // For Java: prepend object if present
        TSNode obj = ts_node_child_by_field_name(node, "object", 6);
        if (!ts_node_is_null(obj) && name) {
            char* obj_text = cbm_node_text(a, obj, source);
            if (obj_text && obj_text[0]) {
                return cbm_arena_sprintf(a, "%s.%s", obj_text, name);
            }
        }
        return name;
    }

    // Ruby: "method" + "receiver" fields
    TSNode method_node = ts_node_child_by_field_name(node, "method", 6);
    if (!ts_node_is_null(method_node)) {
        char* method = cbm_node_text(a, method_node, source);
        TSNode recv = ts_node_child_by_field_name(node, "receiver", 8);
        if (!ts_node_is_null(recv) && method) {
            char* recv_text = cbm_node_text(a, recv, source);
            if (recv_text && recv_text[0]) {
                return cbm_arena_sprintf(a, "%s.%s", recv_text, method);
            }
        }
        return method;
    }

    // Generic fallback: first child
    if (ts_node_child_count(node) > 0) {
        TSNode first = ts_node_child(node, 0);
        if (strcmp(ts_node_type(first), "identifier") == 0) {
            return cbm_node_text(a, first, source);
        }
    }

    return NULL;
}

// Walk AST for call nodes
static void walk_calls_body(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec);
static void walk_calls(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec) {
    if (!cbm_walk_enter(ctx)) return;
    walk_calls_body(ctx, node, spec);
    cbm_walk_leave(ctx);
}

static void walk_calls_body(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec) {
    const char* kind = ts_node_type(node);

    if (cbm_kind_in_set(node, spec->call_node_types)) {
        char* callee = extract_callee_name(ctx->arena, node, ctx->source, ctx->language);
        if (callee && callee[0]) {
            // Skip keywords
            if (!cbm_is_keyword(callee, ctx->language)) {
                CBMCall call;
                call.callee_name = callee;
                call.enclosing_func_qn = cbm_enclosing_func_qn_cached(ctx, node);
                call.dispatch_kind = NULL;
                cbm_calls_push(&ctx->result->calls, ctx->arena, call);

                // Python: Depends(func) — emit the argument as a call target too
                if (ctx->language == CBM_LANG_PYTHON && strcmp(callee, "Depends") == 0) {
                    TSNode args = ts_node_child_by_field_name(node, "arguments", 9);
                    if (!ts_node_is_null(args)) {
                        // First named child of argument_list is the dependency function
                        uint32_t ncount = ts_node_named_child_count(args);
                        if (ncount > 0) {
                            TSNode first_arg = ts_node_named_child(args, 0);
                            if (!ts_node_is_null(first_arg) &&
                                strcmp(ts_node_type(first_arg), "identifier") == 0) {
                                char* dep_name = cbm_node_text(ctx->arena, first_arg, ctx->source);
                                if (dep_name && dep_name[0] && !cbm_is_keyword(dep_name, ctx->language)) {
                                    CBMCall dep_call;
                                    dep_call.callee_name = dep_name;
                                    dep_call.enclosing_func_qn = call.enclosing_func_qn;
                                    dep_call.dispatch_kind = "depends";
                                    cbm_calls_push(&ctx->result->calls, ctx->arena, dep_call);
                                }
                            }
                        }
                    }
                }

                // Python: <pool>.submit(fn, ...) — emit fn as a call target.
                // INDIRECT_CALLS v0.1 (executor.submit only). The most
                // common indirect-dispatch pattern in Python production
                // code (concurrent.futures ThreadPoolExecutor /
                // ProcessPoolExecutor). Without this, every submit(fn)
                // dispatch site is unbound and contributes to the
                // "speculative" confidence_band.
                //
                // Detection: callee_name ends in ".submit" AND the first
                // arg is a bare identifier. Mirrors the existing
                // Depends() pattern. The resolver still drops the edge
                // if fn doesn't resolve to a Function/Method node, so
                // false positives on non-callable args are bounded.
                //
                // See INDIRECT_CALLS_DESIGN.md for v0.2-v0.5 (getattr,
                // decorator, fn-pointer, **kwargs).
                if (ctx->language == CBM_LANG_PYTHON) {
                    size_t cn_len = strlen(callee);
                    if (cn_len >= 7 &&
                        strcmp(callee + cn_len - 7, ".submit") == 0) {
                        TSNode args = ts_node_child_by_field_name(node, "arguments", 9);
                        if (!ts_node_is_null(args)) {
                            uint32_t ncount = ts_node_named_child_count(args);
                            if (ncount > 0) {
                                TSNode first_arg = ts_node_named_child(args, 0);
                                if (!ts_node_is_null(first_arg) &&
                                    strcmp(ts_node_type(first_arg), "identifier") == 0) {
                                    char* sub_name = cbm_node_text(ctx->arena, first_arg, ctx->source);
                                    if (sub_name && sub_name[0] && !cbm_is_keyword(sub_name, ctx->language)) {
                                        CBMCall sub_call;
                                        sub_call.callee_name = sub_name;
                                        sub_call.enclosing_func_qn = call.enclosing_func_qn;
                                        sub_call.dispatch_kind = "executor_submit";
                                        cbm_calls_push(&ctx->result->calls, ctx->arena, sub_call);
                                    }
                                }
                            }
                        }
                    }
                }

                // Python: app.before_request(fn) and the rest of the
                // Flask hook-registrar family — emit fn as a call target.
                // INDIRECT_CALLS v0.3 Pattern A. The registration is
                // an explicit Name reference at the call site; only the
                // dispatch is indirect (Flask invokes from a registered-
                // hook list at request time). Without this, every Flask
                // hook handler has 0 inbound CALLS edges and trace_call_path
                // returns confidence_band=high with unresolved_call_count=0
                // (the extractor sees no callers in the first place).
                //
                // The allowlist is narrow (7 Flask-specific suffixes — see
                // cbm_python_flask_hook_label above). Pattern B (route
                // decorators) and Pattern C (functools.wraps closures) are
                // deferred to v0.4 per INDIRECT_CALLS_V0_3_PLAN.md.
                if (ctx->language == CBM_LANG_PYTHON) {
                    const char* hook_label = cbm_python_flask_hook_label(callee);
                    if (hook_label != NULL) {
                        TSNode args = ts_node_child_by_field_name(node, "arguments", 9);
                        if (!ts_node_is_null(args)) {
                            uint32_t ncount = ts_node_named_child_count(args);
                            if (ncount > 0) {
                                TSNode first_arg = ts_node_named_child(args, 0);
                                if (!ts_node_is_null(first_arg) &&
                                    strcmp(ts_node_type(first_arg), "identifier") == 0) {
                                    char* hook_name = cbm_node_text(ctx->arena, first_arg, ctx->source);
                                    if (hook_name && hook_name[0] && !cbm_is_keyword(hook_name, ctx->language)) {
                                        CBMCall hook_call;
                                        hook_call.callee_name = hook_name;
                                        hook_call.enclosing_func_qn = call.enclosing_func_qn;
                                        hook_call.dispatch_kind = hook_label;
                                        cbm_calls_push(&ctx->result->calls, ctx->arena, hook_call);
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }

        // Python: getattr(obj, "method")(...) — INDIRECT_CALLS v0.2.
        // Symmetric to handle_calls() path; see comment there.
        if (ctx->language == CBM_LANG_PYTHON) {
            TSNode outer_func = ts_node_child_by_field_name(node, "function", 8);
            if (!ts_node_is_null(outer_func) &&
                strcmp(ts_node_type(outer_func), "call") == 0) {
                TSNode inner_func = ts_node_child_by_field_name(outer_func, "function", 8);
                if (!ts_node_is_null(inner_func) &&
                    strcmp(ts_node_type(inner_func), "identifier") == 0) {
                    char* inner_name = cbm_node_text(ctx->arena, inner_func, ctx->source);
                    if (inner_name && strcmp(inner_name, "getattr") == 0) {
                        TSNode inner_args = ts_node_child_by_field_name(outer_func, "arguments", 9);
                        if (!ts_node_is_null(inner_args)) {
                            uint32_t inner_count = ts_node_named_child_count(inner_args);
                            if (inner_count >= 2) {
                                TSNode method_arg = ts_node_named_child(inner_args, 1);
                                if (!ts_node_is_null(method_arg) &&
                                    strcmp(ts_node_type(method_arg), "string") == 0) {
                                    uint32_t scount = ts_node_named_child_count(method_arg);
                                    for (uint32_t si = 0; si < scount; si++) {
                                        TSNode sc = ts_node_named_child(method_arg, si);
                                        if (strcmp(ts_node_type(sc), "string_content") == 0) {
                                            char* method_name = cbm_node_text(ctx->arena, sc, ctx->source);
                                            if (method_name && method_name[0] &&
                                                !cbm_is_keyword(method_name, ctx->language)) {
                                                CBMCall ga_call;
                                                ga_call.callee_name = method_name;
                                                ga_call.enclosing_func_qn =
                                                    cbm_enclosing_func_qn_cached(ctx, node);
                                                ga_call.dispatch_kind = "getattr";
                                                cbm_calls_push(&ctx->result->calls, ctx->arena, ga_call);
                                            }
                                            break;
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
        // Don't recurse into call arguments for nested calls — the walk handles that
    }

    // JSX component refs (TSX/JSX)
    if (ctx->language == CBM_LANG_TSX || ctx->language == CBM_LANG_JAVASCRIPT) {
        if (strcmp(kind, "jsx_self_closing_element") == 0 ||
            strcmp(kind, "jsx_opening_element") == 0) {
            extract_jsx_refs(ctx, node);
        }
    }

    // Recurse
    uint32_t count = ts_node_child_count(node);
    for (uint32_t i = 0; i < count; i++) {
        walk_calls(ctx, ts_node_child(node, i), spec);
    }
}

// Extract JSX component references (uppercase = component, lowercase = HTML)
static void extract_jsx_refs_body(CBMExtractCtx* ctx, TSNode node);
static void extract_jsx_refs(CBMExtractCtx* ctx, TSNode node) {
    if (!cbm_walk_enter(ctx)) return;
    extract_jsx_refs_body(ctx, node);
    cbm_walk_leave(ctx);
}

static void extract_jsx_refs_body(CBMExtractCtx* ctx, TSNode node) {
    TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
    if (ts_node_is_null(name_node)) return;

    char* name = cbm_node_text(ctx->arena, name_node, ctx->source);
    if (!name || !name[0]) return;

    // Only uppercase names are components
    if (name[0] < 'A' || name[0] > 'Z') return;

    CBMCall call;
    call.callee_name = name;
    call.enclosing_func_qn = cbm_enclosing_func_qn_cached(ctx, node);
    call.dispatch_kind = NULL;
    cbm_calls_push(&ctx->result->calls, ctx->arena, call);
}

void cbm_extract_calls(CBMExtractCtx* ctx) {
    const CBMLangSpec* spec = cbm_lang_spec(ctx->language);
    if (!spec || !spec->call_node_types || !spec->call_node_types[0]) return;

    walk_calls(ctx, ctx->root, spec);
}

// --- Unified handler: called once per node by the cursor walk ---

void handle_calls(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec, WalkState* state) {
    if (!spec->call_node_types || !spec->call_node_types[0]) return;

    const char* kind = ts_node_type(node);

    if (cbm_kind_in_set(node, spec->call_node_types)) {
        char* callee = extract_callee_name(ctx->arena, node, ctx->source, ctx->language);
        if (callee && callee[0] && !cbm_is_keyword(callee, ctx->language)) {
            CBMCall call;
            call.callee_name = callee;
            call.enclosing_func_qn = state->enclosing_func_qn;
            call.dispatch_kind = NULL;
            cbm_calls_push(&ctx->result->calls, ctx->arena, call);

            // Python: Depends(func) — emit the argument as a call target too
            if (ctx->language == CBM_LANG_PYTHON && strcmp(callee, "Depends") == 0) {
                TSNode args = ts_node_child_by_field_name(node, "arguments", 9);
                if (!ts_node_is_null(args)) {
                    uint32_t ncount = ts_node_named_child_count(args);
                    if (ncount > 0) {
                        TSNode first_arg = ts_node_named_child(args, 0);
                        if (!ts_node_is_null(first_arg) &&
                            strcmp(ts_node_type(first_arg), "identifier") == 0) {
                            char* dep_name = cbm_node_text(ctx->arena, first_arg, ctx->source);
                            if (dep_name && dep_name[0] && !cbm_is_keyword(dep_name, ctx->language)) {
                                CBMCall dep_call;
                                dep_call.callee_name = dep_name;
                                dep_call.enclosing_func_qn = state->enclosing_func_qn;
                                dep_call.dispatch_kind = "depends";
                                cbm_calls_push(&ctx->result->calls, ctx->arena, dep_call);
                            }
                        }
                    }
                }
            }

            // Python: <pool>.submit(fn, ...) — emit fn as a call target.
            // INDIRECT_CALLS v0.1 (executor.submit only). Mirrors the
            // walk_calls() path above; the unified walker is the
            // production code path that handle_calls() services.
            // See INDIRECT_CALLS_DESIGN.md for v0.2-v0.5.
            if (ctx->language == CBM_LANG_PYTHON) {
                size_t cn_len = strlen(callee);
                if (cn_len >= 7 &&
                    strcmp(callee + cn_len - 7, ".submit") == 0) {
                    TSNode args = ts_node_child_by_field_name(node, "arguments", 9);
                    if (!ts_node_is_null(args)) {
                        uint32_t ncount = ts_node_named_child_count(args);
                        if (ncount > 0) {
                            TSNode first_arg = ts_node_named_child(args, 0);
                            if (!ts_node_is_null(first_arg) &&
                                strcmp(ts_node_type(first_arg), "identifier") == 0) {
                                char* sub_name = cbm_node_text(ctx->arena, first_arg, ctx->source);
                                if (sub_name && sub_name[0] && !cbm_is_keyword(sub_name, ctx->language)) {
                                    CBMCall sub_call;
                                    sub_call.callee_name = sub_name;
                                    sub_call.enclosing_func_qn = state->enclosing_func_qn;
                                    sub_call.dispatch_kind = "executor_submit";
                                    cbm_calls_push(&ctx->result->calls, ctx->arena, sub_call);
                                }
                            }
                        }
                    }
                }
            }

            // Python: app.before_request(fn) family — emit fn as a call
            // target. INDIRECT_CALLS v0.3 Pattern A. Mirrors walk_calls()
            // path above; this is the unified production code path.
            // See cbm_python_flask_hook_label above for the registrar
            // allowlist and INDIRECT_CALLS_V0_3_PLAN.md for design.
            if (ctx->language == CBM_LANG_PYTHON) {
                const char* hook_label = cbm_python_flask_hook_label(callee);
                if (hook_label != NULL) {
                    TSNode args = ts_node_child_by_field_name(node, "arguments", 9);
                    if (!ts_node_is_null(args)) {
                        uint32_t ncount = ts_node_named_child_count(args);
                        if (ncount > 0) {
                            TSNode first_arg = ts_node_named_child(args, 0);
                            if (!ts_node_is_null(first_arg) &&
                                strcmp(ts_node_type(first_arg), "identifier") == 0) {
                                char* hook_name = cbm_node_text(ctx->arena, first_arg, ctx->source);
                                if (hook_name && hook_name[0] && !cbm_is_keyword(hook_name, ctx->language)) {
                                    CBMCall hook_call;
                                    hook_call.callee_name = hook_name;
                                    hook_call.enclosing_func_qn = state->enclosing_func_qn;
                                    hook_call.dispatch_kind = hook_label;
                                    cbm_calls_push(&ctx->result->calls, ctx->arena, hook_call);
                                }
                            }
                        }
                    }
                }
            }
        }

        // Python: getattr(obj, "method")(...) — emit "method" as a call
        // target. INDIRECT_CALLS v0.2.
        //
        // This case fires even when callee is empty (the outer call's
        // function is a `call` node — extract_callee_name returns NULL
        // for nested-call shapes — so we don't get into the callee-
        // emission branch above). Detection is independent of callee
        // extraction.
        //
        // Variable name (`getattr(obj, name_var)`) is not handled — only
        // string-literal method names. Skipping the variable case is the
        // conservative choice; better to under-emit than emit phantom
        // edges to wrong targets.
        if (ctx->language == CBM_LANG_PYTHON) {
            TSNode outer_func = ts_node_child_by_field_name(node, "function", 8);
            if (!ts_node_is_null(outer_func) &&
                strcmp(ts_node_type(outer_func), "call") == 0) {
                // outer_func is itself a call — could be getattr(...)
                TSNode inner_func = ts_node_child_by_field_name(outer_func, "function", 8);
                if (!ts_node_is_null(inner_func) &&
                    strcmp(ts_node_type(inner_func), "identifier") == 0) {
                    char* inner_name = cbm_node_text(ctx->arena, inner_func, ctx->source);
                    if (inner_name && strcmp(inner_name, "getattr") == 0) {
                        TSNode inner_args = ts_node_child_by_field_name(outer_func, "arguments", 9);
                        if (!ts_node_is_null(inner_args)) {
                            uint32_t inner_count = ts_node_named_child_count(inner_args);
                            if (inner_count >= 2) {
                                TSNode method_arg = ts_node_named_child(inner_args, 1);
                                if (!ts_node_is_null(method_arg) &&
                                    strcmp(ts_node_type(method_arg), "string") == 0) {
                                    // string node has children: string_start,
                                    // string_content, string_end. Get the
                                    // string_content.
                                    uint32_t scount = ts_node_named_child_count(method_arg);
                                    for (uint32_t si = 0; si < scount; si++) {
                                        TSNode sc = ts_node_named_child(method_arg, si);
                                        if (strcmp(ts_node_type(sc), "string_content") == 0) {
                                            char* method_name = cbm_node_text(ctx->arena, sc, ctx->source);
                                            if (method_name && method_name[0] &&
                                                !cbm_is_keyword(method_name, ctx->language)) {
                                                CBMCall ga_call;
                                                ga_call.callee_name = method_name;
                                                ga_call.enclosing_func_qn = state->enclosing_func_qn;
                                                ga_call.dispatch_kind = "getattr";
                                                cbm_calls_push(&ctx->result->calls, ctx->arena, ga_call);
                                            }
                                            break;
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    // JSX component refs (TSX/JSX)
    if (ctx->language == CBM_LANG_TSX || ctx->language == CBM_LANG_JAVASCRIPT) {
        if (strcmp(kind, "jsx_self_closing_element") == 0 ||
            strcmp(kind, "jsx_opening_element") == 0) {
            TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
            if (!ts_node_is_null(name_node)) {
                char* name = cbm_node_text(ctx->arena, name_node, ctx->source);
                if (name && name[0] >= 'A' && name[0] <= 'Z') {
                    CBMCall call;
                    call.callee_name = name;
                    call.enclosing_func_qn = state->enclosing_func_qn;
                    call.dispatch_kind = NULL;
                    cbm_calls_push(&ctx->result->calls, ctx->arena, call);
                }
            }
        }
    }
}
