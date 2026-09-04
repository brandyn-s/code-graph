#include "cbm.h"
#include "helpers.h"
#include "lang_specs.h"
#include <string.h>
#include <stdlib.h>
#include <ctype.h>

// Forward declarations
static void extract_func_def(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec);
static void extract_class_def(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec);
static void extract_variables(CBMExtractCtx* ctx, TSNode root, const CBMLangSpec* spec);
static void extract_class_variables(CBMExtractCtx* ctx, TSNode class_node, const CBMLangSpec* spec);
static void extract_rust_impl(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec);
static void extract_class_methods(CBMExtractCtx* ctx, TSNode class_node, const char* class_qn, const CBMLangSpec* spec);

// --- Helpers ---

// Get "name" field from a node
static TSNode func_name_node(TSNode node) {
    return ts_node_child_by_field_name(node, "name", 4);
}

// Resolve the name node for a function, handling language-specific quirks
static TSNode resolve_func_name(TSNode node, CBMLanguage lang, const char* source) {
    const char* kind = ts_node_type(node);

    TSNode name = func_name_node(node);

    if (!ts_node_is_null(name)) return name;

    // PowerShell: no named fields in the grammar — function_statement's
    // name is a `function_name`-typed child; class methods use `simple_name`.
    if (lang == CBM_LANG_POWERSHELL) {
        if (strcmp(kind, "function_statement") == 0) {
            return cbm_find_child_by_kind(node, "function_name");
        }
        if (strcmp(kind, "class_method_definition") == 0) {
            return cbm_find_child_by_kind(node, "simple_name");
        }
    }

    // SQL: create_function — name in object_reference > identifier
    if (lang == CBM_LANG_SQL && strcmp(kind, "create_function") == 0) {
        TSNode obj_ref = cbm_find_child_by_kind(node, "object_reference");
        if (!ts_node_is_null(obj_ref)) {
            TSNode id = cbm_find_child_by_kind(obj_ref, "identifier");
            if (!ts_node_is_null(id)) return id;
        }
        return cbm_find_child_by_kind(node, "identifier");
    }

    // Arrow function: name on parent variable_declarator
    if (strcmp(kind, "arrow_function") == 0) {
        TSNode parent = ts_node_parent(node);
        if (!ts_node_is_null(parent) && strcmp(ts_node_type(parent), "variable_declarator") == 0) {
            return ts_node_child_by_field_name(parent, "name", 4);
        }
    }

    // Makefile: rule — name is first word in the targets group
    if (lang == CBM_LANG_MAKEFILE && strcmp(kind, "rule") == 0) {
        TSNode targets = cbm_find_child_by_kind(node, "targets");
        if (!ts_node_is_null(targets)) {
            uint32_t nc = ts_node_named_child_count(targets);
            if (nc > 0) return ts_node_named_child(targets, 0);
        }
        // Fallback: first word child directly on rule
        return cbm_find_child_by_kind(node, "word");
    }

    // C/C++/CUDA: function_definition — name is inside the declarator chain
    // C grammar: function_definition{declarator:function_declarator{declarator:identifier}}
    if ((lang == CBM_LANG_C || lang == CBM_LANG_CPP || lang == CBM_LANG_CUDA) &&
        strcmp(kind, "function_definition") == 0) {
        TSNode decl = ts_node_child_by_field_name(node, "declarator", 10);
        for (int depth = 0; depth < 8 && !ts_node_is_null(decl); depth++) {
            const char* dk = ts_node_type(decl);
            if (strcmp(dk, "identifier") == 0) return decl;
            // C++ qualified name: Namespace::Function
            if (strcmp(dk, "qualified_identifier") == 0 ||
                strcmp(dk, "scoped_identifier") == 0) {
                TSNode id = cbm_find_child_by_kind(decl, "identifier");
                if (!ts_node_is_null(id)) return id;
                break;
            }
            // Unwrap pointer_declarator, reference_declarator, function_declarator, etc.
            TSNode inner = ts_node_child_by_field_name(decl, "declarator", 10);
            if (ts_node_is_null(inner) && ts_node_named_child_count(decl) > 0)
                inner = ts_node_named_child(decl, 0);
            if (ts_node_is_null(inner)) break;
            decl = inner;
        }
        TSNode null_node = {0};
        return null_node;
    }

    TSNode null_node = {0};
    return null_node;
}

// Check for export_statement ancestor (JS/TS/TSX)
static bool is_js_exported(TSNode node) {
    return cbm_has_ancestor_kind(node, "export_statement", 4);
}

// Extract docstring from the node's leading comment
static const char* extract_docstring(CBMArena* a, TSNode node, const char* source, CBMLanguage lang) {
    // Go: type_spec is inside type_declaration; comment is before type_declaration
    if (lang == CBM_LANG_GO) {
        const char* kind = ts_node_type(node);
        if (strcmp(kind, "type_spec") == 0 || strcmp(kind, "type_alias") == 0) {
            TSNode parent = ts_node_parent(node);
            if (!ts_node_is_null(parent) && strcmp(ts_node_type(parent), "type_declaration") == 0) {
                TSNode pprev = ts_node_prev_sibling(parent);
                if (!ts_node_is_null(pprev)) {
                    const char* ppk = ts_node_type(pprev);
                    if (strcmp(ppk, "comment") == 0 || strcmp(ppk, "block_comment") == 0 ||
                        strcmp(ppk, "line_comment") == 0) {
                        char* text = cbm_node_text(a, pprev, source);
                        if (text && strlen(text) > 500) text[500] = '\0';
                        return text;
                    }
                }
            }
        }
    }

    // Check previous sibling for comment
    TSNode prev = ts_node_prev_sibling(node);
    if (!ts_node_is_null(prev)) {
        const char* pk = ts_node_type(prev);
        if (strcmp(pk, "comment") == 0 ||
            strcmp(pk, "block_comment") == 0 ||
            strcmp(pk, "line_comment") == 0) {
            char* text = cbm_node_text(a, prev, source);
            if (text && strlen(text) > 500) text[500] = '\0';
            return text;
        }
    }

    // Python: docstring as first child expression_statement -> string inside function body
    if (lang == CBM_LANG_PYTHON) {
        TSNode body = ts_node_child_by_field_name(node, "body", 4);
        if (!ts_node_is_null(body) && ts_node_named_child_count(body) > 0) {
            TSNode first = ts_node_named_child(body, 0);
            if (!ts_node_is_null(first) && strcmp(ts_node_type(first), "expression_statement") == 0) {
                if (ts_node_named_child_count(first) > 0) {
                    TSNode str = ts_node_named_child(first, 0);
                    if (!ts_node_is_null(str)) {
                        const char* sk = ts_node_type(str);
                        if (strcmp(sk, "string") == 0 || strcmp(sk, "concatenated_string") == 0) {
                            char* text = cbm_node_text(a, str, source);
                            if (text && strlen(text) > 500) text[500] = '\0';
                            return text;
                        }
                    }
                }
            }
        }
    }
    return NULL;
}

// Extract decorator names from preceding decorator/annotation nodes
static const char** extract_decorators(CBMArena* a, TSNode node, const char* source,
                                        CBMLanguage lang, const CBMLangSpec* spec) {
    if (!spec->decorator_node_types || !spec->decorator_node_types[0]) return NULL;

    // Count decorators (preceding siblings matching decorator types)
    int count = 0;
    TSNode prev = ts_node_prev_sibling(node);
    while (!ts_node_is_null(prev)) {
        if (cbm_kind_in_set(prev, spec->decorator_node_types)) {
            count++;
        } else {
            break; // stop at first non-decorator
        }
        prev = ts_node_prev_sibling(prev);
    }

    // Java: annotations live inside "modifiers" child, not as preceding siblings
    TSNode modifiers = {0};
    int mod_count = 0;
    if (count == 0 && lang == CBM_LANG_JAVA) {
        modifiers = ts_node_child_by_field_name(node, "modifiers", 9);
        if (ts_node_is_null(modifiers)) {
            modifiers = cbm_find_child_by_kind(node, "modifiers");
        }
        if (!ts_node_is_null(modifiers)) {
            uint32_t mc = ts_node_child_count(modifiers);
            for (uint32_t mi = 0; mi < mc; mi++) {
                TSNode mchild = ts_node_child(modifiers, mi);
                if (cbm_kind_in_set(mchild, spec->decorator_node_types)) {
                    mod_count++;
                }
            }
        }
    }

    int total = count + mod_count;
    if (total == 0) return NULL;

    const char** result = (const char**)cbm_arena_alloc(a, sizeof(const char*) * (total + 1));
    if (!result) return NULL;

    int idx = 0;
    // Preceding siblings
    prev = ts_node_prev_sibling(node);
    while (!ts_node_is_null(prev) && idx < count) {
        if (cbm_kind_in_set(prev, spec->decorator_node_types)) {
            result[idx++] = cbm_node_text(a, prev, source);
        } else {
            break;
        }
        prev = ts_node_prev_sibling(prev);
    }
    // Modifiers children
    if (!ts_node_is_null(modifiers)) {
        uint32_t mc = ts_node_child_count(modifiers);
        for (uint32_t mi = 0; mi < mc && idx < total; mi++) {
            TSNode mchild = ts_node_child(modifiers, mi);
            if (cbm_kind_in_set(mchild, spec->decorator_node_types)) {
                result[idx++] = cbm_node_text(a, mchild, source);
            }
        }
    }
    result[idx] = NULL;
    return result;
}

static bool is_ts_heritage_name_kind(const char* kind) {
    return strcmp(kind, "identifier") == 0 ||
           strcmp(kind, "type_identifier") == 0 ||
           strcmp(kind, "nested_type_identifier") == 0 ||
           strcmp(kind, "member_expression") == 0;
}

static TSNode find_ts_heritage_clause(TSNode node, const char* clause_kind) {
    uint32_t count = ts_node_named_child_count(node);
    for (uint32_t i = 0; i < count; i++) {
        TSNode child = ts_node_named_child(node, i);
        if (strcmp(ts_node_type(child), clause_kind) == 0) return child;
        TSNode nested = find_ts_heritage_clause(child, clause_kind);
        if (!ts_node_is_null(nested)) return nested;
    }
    TSNode null_node = {0};
    return null_node;
}

static void collect_ts_heritage_names(
    CBMArena* a, TSNode node, const char* source, const char* excluded_kind,
    const char** names, int* count
) {
    uint32_t child_count = ts_node_named_child_count(node);
    for (uint32_t i = 0; i < child_count && *count < 63; i++) {
        TSNode child = ts_node_named_child(node, i);
        const char* kind = ts_node_type(child);
        if (excluded_kind && strcmp(kind, excluded_kind) == 0) continue;
        if (strcmp(kind, "type_arguments") == 0) continue;
        if (is_ts_heritage_name_kind(kind)) {
            char* text = cbm_node_text(a, child, source);
            if (text && text[0]) names[(*count)++] = text;
            continue;
        }
        collect_ts_heritage_names(a, child, source, excluded_kind, names, count);
    }
}

static const char** extract_ts_heritage_clause(
    CBMArena* a, TSNode node, const char* source, const char* clause_kind
) {
    TSNode clause = find_ts_heritage_clause(node, clause_kind);
    if (ts_node_is_null(clause)) return NULL;

    const char* names[64];
    int count = 0;
    collect_ts_heritage_names(a, clause, source,
        strcmp(clause_kind, "class_heritage") == 0 ? "implements_clause" : NULL,
        names, &count);
    if (count == 0) return NULL;

    const char** result = (const char**)cbm_arena_alloc(a, sizeof(const char*) * (count + 1));
    if (!result) return NULL;
    for (int i = 0; i < count; i++) result[i] = names[i];
    result[count] = NULL;
    return result;
}

// Extract base class names from a class node
static const char** extract_base_classes(CBMArena* a, TSNode node, const char* source, CBMLanguage lang) {
    // Try common field names for superclass lists
    static const char* fields[] = {"superclass","superclasses","superinterfaces",
                                     "interfaces","bases","type_inheritance_clause",
                                     "delegation_specifiers",NULL};

    for (const char** f = fields; *f; f++) {
        TSNode super = ts_node_child_by_field_name(node, *f, (uint32_t)strlen(*f));
        if (!ts_node_is_null(super)) {
            char* text = cbm_node_text(a, super, source);
            if (text && text[0]) {
                const char** result = (const char**)cbm_arena_alloc(a, sizeof(const char*) * 2);
                if (result) {
                    result[0] = text;
                    result[1] = NULL;
                    return result;
                }
            }
        }
    }
    // Fallback: search for common base class node types as children
    static const char* base_types[] = {
        "superclass","superinterfaces","type_inheritance_clause",
        "class_heritage","delegation_specifiers","super_interfaces",
        "extends_clause","implements_clause","argument_list",
        "inheritance_specifier",NULL
    };
    uint32_t count = ts_node_child_count(node);
    for (uint32_t i = 0; i < count; i++) {
        TSNode child = ts_node_child(node, i);
        const char* ck = ts_node_type(child);
        for (const char** t = base_types; *t; t++) {
            if (strcmp(ck, *t) == 0) {
                char* text = cbm_node_text(a, child, source);
                if (text && text[0]) {
                    const char** result = (const char**)cbm_arena_alloc(a, sizeof(const char*) * 2);
                    if (result) {
                        result[0] = text;
                        result[1] = NULL;
                        return result;
                    }
                }
            }
        }
    }
    return NULL;
}

// Classify class label from AST node kind
static const char* class_label_for_kind(const char* kind) {
    if (strcmp(kind, "interface_declaration") == 0 ||
        strcmp(kind, "interface_type") == 0 ||
        strcmp(kind, "trait_item") == 0 ||
        strcmp(kind, "trait_definition") == 0 ||
        strcmp(kind, "protocol_declaration") == 0) {
        return "Interface";
    }
    if (strcmp(kind, "enum_specifier") == 0 ||
        strcmp(kind, "enum_declaration") == 0 ||
        strcmp(kind, "enum_item") == 0) {
        return "Enum";
    }
    if (strcmp(kind, "type_alias_declaration") == 0 ||
        strcmp(kind, "type_item") == 0 ||
        strcmp(kind, "type_alias") == 0 ||
        strcmp(kind, "type_definition") == 0) {
        return "Type";
    }
    return "Class";
}

// --- Parameter type extraction ---

// Builtin types we skip (not useful as USES_TYPE targets).
static bool is_builtin_type(const char* name) {
    static const char* builtins[] = {
        "int","int8","int16","int32","int64",
        "uint","uint8","uint16","uint32","uint64",
        "float","float32","float64","double",
        "string","str","bool","boolean","byte","rune",
        "void","None","any","interface","object","Object",
        "error","uintptr","complex64","complex128",
        "number","bigint","symbol","undefined","null",
        "char","short","long","i8","i16","i32","i64",
        "u8","u16","u32","u64","f32","f64","usize","isize",
        "self","Self","cls","type",
        "Int","Int8","Int16","Int32","Int64",
        "UInt","UInt8","UInt16","UInt32","UInt64",
        "Float","Double","String","Bool","Boolean",
        "Byte","Short","Long","Char","Unit","Void",
        "Any","Nothing","Dynamic",
        NULL
    };
    for (const char** b = builtins; *b; b++) {
        if (strcmp(name, *b) == 0) return true;
    }
    return false;
}

// Clean a type name: strip *, &, [], ..., generics
static char* clean_type_name(CBMArena* a, const char* raw) {
    if (!raw || !raw[0]) return NULL;
    const char* s = raw;
    // Skip leading whitespace, ":", "*", "&", "[]", "..."
    while (*s == ' ' || *s == '\t' || *s == ':' || *s == '*' ||
           *s == '&' || *s == '[' || *s == ']' || *s == '.') s++;
    if (!*s) return NULL;
    // Find end: stop at <, [, or whitespace
    size_t len = 0;
    while (s[len] && s[len] != '<' && s[len] != '[' && s[len] != ' ') len++;
    if (len == 0) return NULL;
    char* result = cbm_arena_alloc(a, len + 1);
    memcpy(result, s, len);
    result[len] = '\0';
    return result;
}

// Extract param_names from a parameter list node.
// Returns NULL-terminated arena-allocated array.
static const char** extract_param_names(CBMArena* a, TSNode params, const char* source, CBMLanguage lang) {
    if (ts_node_is_null(params)) return NULL;

    const char* names[32];
    int count = 0;

    uint32_t nc = ts_node_child_count(params);
    for (uint32_t i = 0; i < nc && count < 31; i++) {
        TSNode param = ts_node_child(params, i);
        if (ts_node_is_null(param) || !ts_node_is_named(param)) continue;

        const char* pk = ts_node_type(param);
        char* name_text = NULL;

        // Go: parameter_declaration has "name" field
        if (strcmp(pk, "parameter_declaration") == 0) {
            TSNode nm = ts_node_child_by_field_name(param, "name", 4);
            if (!ts_node_is_null(nm)) name_text = cbm_node_text(a, nm, source);
        }
        // Generic: try "name" field on parameter nodes
        else if (strcmp(pk, "formal_parameter") == 0 || strcmp(pk, "parameter") == 0 ||
                 strcmp(pk, "required_parameter") == 0 || strcmp(pk, "optional_parameter") == 0 ||
                 strcmp(pk, "simple_parameter") == 0 || strcmp(pk, "typed_parameter") == 0) {
            TSNode nm = ts_node_child_by_field_name(param, "name", 4);
            if (ts_node_is_null(nm)) nm = ts_node_child_by_field_name(param, "pattern", 7);
            // Python typed_parameter has no "name" field — first named child is the identifier
            if (ts_node_is_null(nm)) nm = ts_node_named_child(param, 0);
            if (!ts_node_is_null(nm)) {
                if (strcmp(ts_node_type(nm), "identifier") == 0 ||
                    strcmp(ts_node_type(nm), "simple_identifier") == 0) {
                    name_text = cbm_node_text(a, nm, source);
                }
            }
        }
        // Bare identifier (Python simple params, JS params)
        else if (strcmp(pk, "identifier") == 0) {
            name_text = cbm_node_text(a, param, source);
        }
        // Python typed_default_parameter: name: type = default
        else if (strcmp(pk, "typed_default_parameter") == 0) {
            TSNode nm = ts_node_child_by_field_name(param, "name", 4);
            if (!ts_node_is_null(nm)) name_text = cbm_node_text(a, nm, source);
        }
        // Python default_parameter: name = default
        else if (strcmp(pk, "default_parameter") == 0) {
            TSNode nm = ts_node_child_by_field_name(param, "name", 4);
            if (!ts_node_is_null(nm)) name_text = cbm_node_text(a, nm, source);
        }
        // JS assignment_pattern: name = default
        else if (strcmp(pk, "assignment_pattern") == 0) {
            TSNode left = ts_node_child_by_field_name(param, "left", 4);
            if (!ts_node_is_null(left) && strcmp(ts_node_type(left), "identifier") == 0) {
                name_text = cbm_node_text(a, left, source);
            }
        }

        if (name_text && name_text[0]) {
            names[count++] = name_text;
        }
    }

    if (count == 0) return NULL;

    const char** result = (const char**)cbm_arena_alloc(a, (count + 1) * sizeof(const char*));
    for (int i = 0; i < count; i++) result[i] = names[i];
    result[count] = NULL;
    return result;
}

// Extract return_types from a return type node.
// Parses Go-style multi-return (T1, T2) and single return types.
// Returns NULL-terminated arena-allocated array.
static const char** extract_return_types(CBMArena* a, TSNode rt_node, const char* source, CBMLanguage lang) {
    if (ts_node_is_null(rt_node)) return NULL;

    const char* types[16];
    int count = 0;

    const char* kind = ts_node_type(rt_node);

    // Go: parameter_list as result type means multi-return
    if (strcmp(kind, "parameter_list") == 0) {
        uint32_t nc = ts_node_child_count(rt_node);
        for (uint32_t i = 0; i < nc && count < 15; i++) {
            TSNode child = ts_node_child(rt_node, i);
            if (ts_node_is_null(child) || !ts_node_is_named(child)) continue;
            const char* ck = ts_node_type(child);
            if (strcmp(ck, "parameter_declaration") == 0) {
                // Get the type from the parameter_declaration
                TSNode tn = ts_node_child_by_field_name(child, "type", 4);
                if (!ts_node_is_null(tn)) {
                    char* type_text = cbm_node_text(a, tn, source);
                    if (type_text && type_text[0]) {
                        char* cleaned = clean_type_name(a, type_text);
                        if (cleaned && cleaned[0]) types[count++] = cleaned;
                    }
                }
            } else {
                // Bare type in result list
                char* type_text = cbm_node_text(a, child, source);
                if (type_text && type_text[0]) {
                    char* cleaned = clean_type_name(a, type_text);
                    if (cleaned && cleaned[0]) types[count++] = cleaned;
                }
            }
        }
    } else {
        // Single return type
        char* type_text = cbm_node_text(a, rt_node, source);
        if (type_text && type_text[0]) {
            char* cleaned = clean_type_name(a, type_text);
            if (cleaned && cleaned[0]) types[count++] = cleaned;
        }
    }

    if (count == 0) return NULL;

    const char** result = (const char**)cbm_arena_alloc(a, (count + 1) * sizeof(const char*));
    for (int i = 0; i < count; i++) result[i] = types[i];
    result[count] = NULL;
    return result;
}

// Extract param_types from a parameter list node.
// Returns NULL-terminated arena-allocated array.
static const char** extract_param_types(CBMArena* a, TSNode params, const char* source, CBMLanguage lang) {
    if (ts_node_is_null(params)) return NULL;

    // Temporary buffer (max 32 param types)
    const char* types[32];
    int count = 0;

    uint32_t nc = ts_node_child_count(params);
    for (uint32_t i = 0; i < nc && count < 31; i++) {
        TSNode param = ts_node_child(params, i);
        if (ts_node_is_null(param) || !ts_node_is_named(param)) continue;

        const char* pk = ts_node_type(param);
        char* type_text = NULL;

        switch (lang) {
        case CBM_LANG_TYPESCRIPT:
        case CBM_LANG_TSX: {
            // TS: required_parameter/optional_parameter -> type_annotation child
            if (strcmp(pk, "required_parameter") == 0 || strcmp(pk, "optional_parameter") == 0) {
                TSNode ta = cbm_find_child_by_kind(param, "type_annotation");
                if (!ts_node_is_null(ta)) {
                    // type_annotation contains ": Type" — get the type identifier
                    uint32_t tanc = ts_node_named_child_count(ta);
                    for (uint32_t ti = 0; ti < tanc; ti++) {
                        TSNode tch = ts_node_named_child(ta, ti);
                        if (!ts_node_is_null(tch)) {
                            const char* tk = ts_node_type(tch);
                            if (strcmp(tk, "type_identifier") == 0 || strcmp(tk, "generic_type") == 0 ||
                                strcmp(tk, "predefined_type") == 0) {
                                type_text = cbm_node_text(a, tch, source);
                                break;
                            }
                        }
                    }
                }
            }
            break;
        }
        default: {
            // Generic: formal_parameter, parameter, parameter_declaration,
            // spread_parameter, simple_parameter, variadic_parameter -> "type" field
            if (strcmp(pk, "formal_parameter") == 0 || strcmp(pk, "parameter") == 0 ||
                strcmp(pk, "parameter_declaration") == 0 || strcmp(pk, "spread_parameter") == 0 ||
                strcmp(pk, "simple_parameter") == 0 || strcmp(pk, "variadic_parameter") == 0 ||
                strcmp(pk, "typed_parameter") == 0 || strcmp(pk, "typed_default_parameter") == 0) {
                TSNode tn = ts_node_child_by_field_name(param, "type", 4);
                if (!ts_node_is_null(tn)) {
                    type_text = cbm_node_text(a, tn, source);
                }
            }
            break;
        }
        }

        if (type_text && type_text[0]) {
            char* cleaned = clean_type_name(a, type_text);
            if (cleaned && cleaned[0] && !is_builtin_type(cleaned)) {
                // Deduplicate
                bool dup = false;
                for (int j = 0; j < count; j++) {
                    if (strcmp(types[j], cleaned) == 0) { dup = true; break; }
                }
                if (!dup) types[count++] = cleaned;
            }
        }
    }

    if (count == 0) return NULL;

    // Build NULL-terminated array
    const char** result = (const char**)cbm_arena_alloc(a, (count + 1) * sizeof(const char*));
    for (int i = 0; i < count; i++) result[i] = types[i];
    result[count] = NULL;
    return result;
}

// --- Function definition extraction ---

static void extract_func_def(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec) {
    CBMArena* a = ctx->arena;

    TSNode name_node = resolve_func_name(node, ctx->language, ctx->source);
    if (ts_node_is_null(name_node)) return;

    char* name = cbm_node_text(a, name_node, ctx->source);
    if (!name || !name[0] || strcmp(name, "function") == 0) return;

    CBMDefinition def;
    memset(&def, 0, sizeof(def));

    def.name = name;
    def.qualified_name = cbm_fqn_compute(a, ctx->project, ctx->rel_path, name);
    def.label = "Function";
    def.file_path = ctx->rel_path;
    def.start_line = ts_node_start_point(node).row + 1;
    def.end_line = ts_node_end_point(node).row + 1;
    def.lines = (int)(def.end_line - def.start_line + 1);
    def.is_exported = cbm_is_exported(name, ctx->language);

    // Parameters
    TSNode params = ts_node_child_by_field_name(node, "parameters", 10);
    if (!ts_node_is_null(params)) {
        def.signature = cbm_node_text(a, params, ctx->source);
        def.param_names = extract_param_names(a, params, ctx->source, ctx->language);
        def.param_types = extract_param_types(a, params, ctx->source, ctx->language);
    }

    // Return type
    static const char* rt_fields[] = {"result","return_type","type",NULL};
    for (const char** f = rt_fields; *f; f++) {
        TSNode rt = ts_node_child_by_field_name(node, *f, (uint32_t)strlen(*f));
        if (!ts_node_is_null(rt)) {
            def.return_type = cbm_node_text(a, rt, ctx->source);
            def.return_types = extract_return_types(a, rt, ctx->source, ctx->language);
            break;
        }
    }

    // Receiver (Go methods). For `func (s *Store) Foo()`, the receiver field
    // contains the full `(s *Store)` expression. We extract the type name
    // (Store) and embed it in the QN: `<file>.<Store>.<method>` instead of
    // `<file>.<method>`. Without this, all methods on different Go types
    // with the same method name collapse to one node (e.g., `Close` on
    // *Cache and *Config both became `<file>.Close`, losing disambiguation).
    // Measured empirically 2026-04-24: code-graph stored store.Querier.QueryContext
    // (with receiver) inconsistently vs edges.EdgeCountsByType (without).
    // Making every method QN receiver-qualified fixes the inconsistency.
    TSNode recv = ts_node_child_by_field_name(node, "receiver", 8);
    if (!ts_node_is_null(recv)) {
        def.receiver = cbm_node_text(a, recv, ctx->source);
        def.label = "Method";
        // Extract just the type name from the receiver. The receiver node
        // is a parameter_list containing parameter_declarations. Walk for
        // the type node.
        const char* recv_type = NULL;
        uint32_t rn = ts_node_child_count(recv);
        for (uint32_t ri = 0; ri < rn; ri++) {
            TSNode p = ts_node_child(recv, ri);
            if (strcmp(ts_node_type(p), "parameter_declaration") != 0) continue;
            TSNode tnode = ts_node_child_by_field_name(p, "type", 4);
            if (ts_node_is_null(tnode)) continue;
            char* tn = cbm_node_text(a, tnode, ctx->source);
            if (!tn || !tn[0]) continue;
            // Strip pointer prefix (`*Store` -> `Store`) and generic args
            // (`Store[T]` -> `Store`).
            while (*tn == '*' || *tn == '&') tn++;
            char* bracket = strchr(tn, '[');
            if (bracket) *bracket = '\0';
            recv_type = tn;
            break;
        }
        if (recv_type && recv_type[0]) {
            // Reconstruct QN with receiver type: <module>.<Type>.<method>.
            // cbm_fqn_compute(empty name) returns the module QN with a
            // trailing "."; strip it before splicing in the receiver.
            const char* module_qn = cbm_fqn_compute(a, ctx->project, ctx->rel_path, "");
            size_t mlen = strlen(module_qn);
            if (mlen > 0 && module_qn[mlen-1] == '.') {
                char* trimmed = cbm_arena_strndup(a, module_qn, mlen - 1);
                def.qualified_name = cbm_arena_sprintf(a, "%s.%s.%s", trimmed, recv_type, name);
            } else {
                def.qualified_name = cbm_arena_sprintf(a, "%s.%s.%s", module_qn, recv_type, name);
            }
            def.parent_class = recv_type;
        }
    }

    // Decorators
    def.decorators = extract_decorators(a, node, ctx->source, ctx->language, spec);

    // Docstring
    def.docstring = extract_docstring(a, node, ctx->source, ctx->language);

    // Complexity
    if (spec->branching_node_types && spec->branching_node_types[0]) {
        def.complexity = cbm_count_branching(node, spec->branching_node_types);
    }

    // JS/TS export detection
    if (ctx->language == CBM_LANG_JAVASCRIPT || ctx->language == CBM_LANG_TYPESCRIPT || ctx->language == CBM_LANG_TSX) {
        if (is_js_exported(node)) {
            def.is_entry_point = true;
        }
    }

    // main is always an entry point
    if (strcmp(name, "main") == 0) {
        def.is_entry_point = true;
    }

    cbm_defs_push(&ctx->result->defs, a, def);
}

// --- Class definition extraction ---

static void extract_class_def(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec) {
    CBMArena* a = ctx->arena;
    const char* kind = ts_node_type(node);

    // Config language class extraction (TOML tables, INI sections, XML elements, Markdown headings)
    if (ctx->language == CBM_LANG_TOML &&
        (strcmp(kind, "table") == 0 || strcmp(kind, "table_array_element") == 0)) {
        // TOML table: name from first bare_key/dotted_key/quoted_key child,
        // or from the nested key within a bracket header
        char* name = NULL;
        uint32_t nc = ts_node_child_count(node);
        for (uint32_t i = 0; i < nc && !name; i++) {
            TSNode child = ts_node_child(node, i);
            const char* ck = ts_node_type(child);
            if (strcmp(ck, "bare_key") == 0 || strcmp(ck, "dotted_key") == 0 ||
                strcmp(ck, "quoted_key") == 0 || strcmp(ck, "key") == 0) {
                name = cbm_node_text(a, child, ctx->source);
            }
        }
        if (!name || !name[0]) return;
        CBMDefinition def;
        memset(&def, 0, sizeof(def));
        def.name = name;
        def.qualified_name = cbm_fqn_compute(a, ctx->project, ctx->rel_path, name);
        def.label = "Class";
        def.file_path = ctx->rel_path;
        def.start_line = ts_node_start_point(node).row + 1;
        def.end_line = ts_node_end_point(node).row + 1;
        def.is_exported = true;
        cbm_defs_push(&ctx->result->defs, a, def);
        return;
    }

    if (ctx->language == CBM_LANG_XML && strcmp(kind, "element") == 0) {
        // XML element: name from start_tag > tag_name or self_closing_tag > tag_name
        char* name = NULL;
        uint32_t nc = ts_node_child_count(node);
        for (uint32_t i = 0; i < nc && !name; i++) {
            TSNode child = ts_node_child(node, i);
            const char* ck = ts_node_type(child);
            if (strcmp(ck, "start_tag") == 0 || strcmp(ck, "self_closing_tag") == 0 ||
                strcmp(ck, "STag") == 0 || strcmp(ck, "EmptyElemTag") == 0) {
                // Find Name or tag_name child
                uint32_t tnc = ts_node_child_count(child);
                for (uint32_t j = 0; j < tnc; j++) {
                    TSNode tag = ts_node_child(child, j);
                    const char* tk = ts_node_type(tag);
                    if (strcmp(tk, "tag_name") == 0 || strcmp(tk, "Name") == 0) {
                        name = cbm_node_text(a, tag, ctx->source);
                        break;
                    }
                }
            }
        }
        // Fallback: try "Name" field directly for some XML grammars
        if (!name) {
            TSNode name_child = cbm_find_child_by_kind(node, "Name");
            if (!ts_node_is_null(name_child)) {
                name = cbm_node_text(a, name_child, ctx->source);
            }
        }
        if (!name || !name[0]) return;
        CBMDefinition def;
        memset(&def, 0, sizeof(def));
        def.name = name;
        def.qualified_name = cbm_fqn_compute(a, ctx->project, ctx->rel_path, name);
        def.label = "Class";
        def.file_path = ctx->rel_path;
        def.start_line = ts_node_start_point(node).row + 1;
        def.end_line = ts_node_end_point(node).row + 1;
        def.is_exported = true;
        cbm_defs_push(&ctx->result->defs, a, def);
        return;
    }

    if (ctx->language == CBM_LANG_MARKDOWN &&
        (strcmp(kind, "atx_heading") == 0 || strcmp(kind, "setext_heading") == 0)) {
        // Markdown heading: extract text content as name, use "Section" label
        // For atx_heading: children are atx_h[1-6]_marker + inline content
        // For setext_heading: children are paragraph + setext_h[12]_underline
        char* name = NULL;
        if (strcmp(kind, "atx_heading") == 0) {
            // Find heading_content or inline child (skip the marker)
            uint32_t nc = ts_node_child_count(node);
            for (uint32_t i = 0; i < nc; i++) {
                TSNode child = ts_node_child(node, i);
                const char* ck = ts_node_type(child);
                if (strcmp(ck, "heading_content") == 0 || strcmp(ck, "inline") == 0) {
                    name = cbm_node_text(a, child, ctx->source);
                    break;
                }
            }
            // Fallback: extract everything after the # marker
            if (!name) {
                char* full = cbm_node_text(a, node, ctx->source);
                if (full) {
                    // Skip leading # and space
                    char* p = full;
                    while (*p == '#') p++;
                    while (*p == ' ') p++;
                    if (*p) name = cbm_arena_strdup(a, p);
                }
            }
        } else {
            // setext_heading: first child is the heading text (paragraph)
            if (ts_node_child_count(node) > 0) {
                TSNode first = ts_node_child(node, 0);
                const char* fk = ts_node_type(first);
                if (strcmp(fk, "paragraph") == 0 || strcmp(fk, "heading_content") == 0 ||
                    strcmp(fk, "inline") == 0) {
                    name = cbm_node_text(a, first, ctx->source);
                } else {
                    name = cbm_node_text(a, first, ctx->source);
                }
            }
        }
        if (!name || !name[0]) return;
        // Trim trailing whitespace/newlines
        size_t len = strlen(name);
        while (len > 0 && (name[len-1] == '\n' || name[len-1] == '\r' || name[len-1] == ' ')) {
            name[len-1] = '\0';
            len--;
        }
        if (!name[0]) return;
        CBMDefinition def;
        memset(&def, 0, sizeof(def));
        def.name = name;
        def.qualified_name = cbm_fqn_compute(a, ctx->project, ctx->rel_path, name);
        def.label = "Section";  // NOT "Class" — avoids polluting class queries
        def.file_path = ctx->rel_path;
        def.start_line = ts_node_start_point(node).row + 1;
        def.end_line = ts_node_end_point(node).row + 1;
        def.is_exported = true;
        cbm_defs_push(&ctx->result->defs, a, def);
        return;
    }

    // HCL blocks need special handling
    if (ctx->language == CBM_LANG_HCL && strcmp(kind, "block") == 0) {
        // Simple: use first identifier child as name
        TSNode id = cbm_find_child_by_kind(node, "identifier");
        if (ts_node_is_null(id)) return;
        char* name = cbm_node_text(a, id, ctx->source);
        if (!name || !name[0]) return;

        CBMDefinition def;
        memset(&def, 0, sizeof(def));
        def.name = name;
        def.qualified_name = cbm_fqn_compute(a, ctx->project, ctx->rel_path, name);
        def.label = "Class";
        def.file_path = ctx->rel_path;
        def.start_line = ts_node_start_point(node).row + 1;
        def.end_line = ts_node_end_point(node).row + 1;
        def.is_exported = true;
        cbm_defs_push(&ctx->result->defs, a, def);
        return;
    }

    TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
    // PowerShell: the grammar has no named fields — class_statement's name
    // is its first `simple_name`-typed child.
    if (ts_node_is_null(name_node) && ctx->language == CBM_LANG_POWERSHELL) {
        name_node = cbm_find_child_by_kind(node, "simple_name");
    }
    if (ts_node_is_null(name_node)) return;

    char* name = cbm_node_text(a, name_node, ctx->source);
    if (!name || !name[0]) return;

    const char* class_qn = cbm_fqn_compute(a, ctx->project, ctx->rel_path, name);
    const char* label = class_label_for_kind(kind);

    // Go type_spec: check inner type for interface/struct
    if (strcmp(kind, "type_spec") == 0) {
        TSNode type_inner = ts_node_child_by_field_name(node, "type", 4);
        if (!ts_node_is_null(type_inner)) {
            const char* inner_kind = ts_node_type(type_inner);
            if (strcmp(inner_kind, "interface_type") == 0) label = "Interface";
            else if (strcmp(inner_kind, "struct_type") == 0) label = "Class";
        }
    }

    CBMDefinition def;
    memset(&def, 0, sizeof(def));
    def.name = name;
    def.qualified_name = class_qn;
    def.label = label;
    def.file_path = ctx->rel_path;
    def.start_line = ts_node_start_point(node).row + 1;
    def.end_line = ts_node_end_point(node).row + 1;
    def.is_exported = cbm_is_exported(name, ctx->language);
    def.base_classes = extract_base_classes(a, node, ctx->source, ctx->language);
    if (ctx->language == CBM_LANG_TYPESCRIPT || ctx->language == CBM_LANG_TSX) {
        def.extends_types = extract_ts_heritage_clause(a, node, ctx->source,
            strcmp(kind, "interface_declaration") == 0 ? "extends_type_clause" : "class_heritage");
        def.implements_types = extract_ts_heritage_clause(a, node, ctx->source, "implements_clause");
    }
    def.decorators = extract_decorators(a, node, ctx->source, ctx->language, spec);
    def.docstring = extract_docstring(a, node, ctx->source, ctx->language);

    cbm_defs_push(&ctx->result->defs, a, def);

    // Extract methods inside the class
    extract_class_methods(ctx, node, class_qn, spec);

    // Extract class-level variables (field declarations)
    extract_class_variables(ctx, node, spec);
}

// Find the body/members node inside a class node
static TSNode find_class_body(TSNode class_node, CBMLanguage lang) {
    // PowerShell: class_statement has no body wrapper — method/property
    // members are direct children, so the class node IS the body.
    if (lang == CBM_LANG_POWERSHELL &&
        strcmp(ts_node_type(class_node), "class_statement") == 0) {
        return class_node;
    }
    // Try field names first
    static const char* body_fields[] = {"body","members","class_body","declaration_list",NULL};
    for (const char** f = body_fields; *f; f++) {
        TSNode body = ts_node_child_by_field_name(class_node, *f, (uint32_t)strlen(*f));
        if (!ts_node_is_null(body)) return body;
    }
    // Go: type_spec -> type field (interface_type or struct_type)
    if (lang == CBM_LANG_GO) {
        TSNode type_inner = ts_node_child_by_field_name(class_node, "type", 4);
        if (!ts_node_is_null(type_inner)) return type_inner;
    }
    // Fallback: search children for known body node types
    static const char* body_types[] = {
        "class_body","interface_body","enum_body","template_body",
        "interface_type","struct_type","field_declaration_list",
        "compound_statement","block","closure",
        "implementation_definition",NULL
    };
    uint32_t count = ts_node_child_count(class_node);
    for (uint32_t i = 0; i < count; i++) {
        TSNode child = ts_node_child(class_node, i);
        const char* ck = ts_node_type(child);
        for (const char** t = body_types; *t; t++) {
            if (strcmp(ck, *t) == 0) return child;
        }
    }
    TSNode null_node = {0};
    return null_node;
}

// Helper: try to extract method name from a node, with fallbacks
static TSNode resolve_method_name(TSNode child, CBMLanguage lang) {
    // PowerShell class methods: name is a `simple_name`-typed child (the
    // grammar has no named fields).
    if (lang == CBM_LANG_POWERSHELL) {
        TSNode nm = cbm_find_child_by_kind(child, "simple_name");
        if (!ts_node_is_null(nm)) return nm;
    }
    TSNode name_node = func_name_node(child);
    if (!ts_node_is_null(name_node)) return name_node;

    const char* ck = ts_node_type(child);

    // Arrow function: name on parent variable_declarator
    if (strcmp(ck, "arrow_function") == 0) {
        TSNode parent = ts_node_parent(child);
        if (!ts_node_is_null(parent)) {
            const char* pk = ts_node_type(parent);
            if (strcmp(pk, "field_definition") == 0) {
                return ts_node_child_by_field_name(parent, "property", 8);
            } else if (strcmp(pk, "public_field_definition") == 0 || strcmp(pk, "variable_declarator") == 0) {
                return ts_node_child_by_field_name(parent, "name", 4);
            }
        }
    }

    TSNode null_node = {0};
    return null_node;
}

// Push a single method definition
static void push_method_def(CBMExtractCtx* ctx, TSNode child, const char* class_qn,
                             const CBMLangSpec* spec, TSNode name_node) {
    CBMArena* a = ctx->arena;

    char* name = cbm_node_text(a, name_node, ctx->source);
    if (!name || !name[0]) return;

    const char* method_qn = cbm_arena_sprintf(a, "%s.%s", class_qn, name);

    CBMDefinition def;
    memset(&def, 0, sizeof(def));
    def.name = name;
    def.qualified_name = method_qn;
    def.label = "Method";
    def.file_path = ctx->rel_path;
    def.parent_class = class_qn;
    def.start_line = ts_node_start_point(child).row + 1;
    def.end_line = ts_node_end_point(child).row + 1;
    def.lines = (int)(def.end_line - def.start_line + 1);
    def.is_exported = cbm_is_exported(name, ctx->language);

    TSNode params = ts_node_child_by_field_name(child, "parameters", 10);
    if (!ts_node_is_null(params)) {
        def.signature = cbm_node_text(a, params, ctx->source);
        def.param_types = extract_param_types(a, params, ctx->source, ctx->language);
    }

    def.decorators = extract_decorators(a, child, ctx->source, ctx->language, spec);
    def.docstring = extract_docstring(a, child, ctx->source, ctx->language);

    if (spec->branching_node_types && spec->branching_node_types[0]) {
        def.complexity = cbm_count_branching(child, spec->branching_node_types);
    }

    cbm_defs_push(&ctx->result->defs, a, def);
}

// Extract methods inside a class body
static void extract_class_methods(CBMExtractCtx* ctx, TSNode class_node,
                                   const char* class_qn, const CBMLangSpec* spec) {
    TSNode body = find_class_body(class_node, ctx->language);
    if (ts_node_is_null(body)) return;

    uint32_t count = ts_node_child_count(body);
    for (uint32_t i = 0; i < count; i++) {
        TSNode child = ts_node_child(body, i);
        if (ts_node_is_null(child)) continue;

        // Python: decorated methods are wrapped in decorated_definition.
        // The class body contains `decorated_definition` whose
        // `function_definition` child is the actual method. Without this
        // unwrap, every @staticmethod / @classmethod / @property /
        // @custom_decorator method on the class is silently skipped.
        // Bug observed on pandas StringMethods._validate (PR #94).
        if (ctx->language == CBM_LANG_PYTHON &&
            strcmp(ts_node_type(child), "decorated_definition") == 0) {
            uint32_t nc = ts_node_child_count(child);
            for (uint32_t j = 0; j < nc; j++) {
                TSNode inner = ts_node_child(child, j);
                if (ts_node_is_null(inner)) continue;
                if (cbm_kind_in_set(inner, spec->function_node_types)) {
                    TSNode nm = resolve_method_name(inner, ctx->language);
                    if (!ts_node_is_null(nm)) {
                        push_method_def(ctx, inner, class_qn, spec, nm);
                    }
                    break; // one function_definition per decorated_definition
                }
            }
            continue;
        }

        if (!cbm_kind_in_set(child, spec->function_node_types)) continue;

        TSNode name_node = resolve_method_name(child, ctx->language);
        if (ts_node_is_null(name_node)) continue;

        push_method_def(ctx, child, class_qn, spec, name_node);
    }
}

// --- Rust impl block extraction ---

// Strip generic-parameter suffix from a Rust type name in place.
// `TailscaleAuthService<S>` -> `TailscaleAuthService`
// `Foo<T, U>`               -> `Foo`
// `Result<X, Y>`            -> `Result`
// Also handles whitespace before the `<`. Returns input unchanged if no '<'.
//
// Why: `impl<S> ... for TailscaleAuthService<S>` produces a type-name token
// `TailscaleAuthService<S>`. Composing `TailscaleAuthService<S>.call` as the
// method's QN diverges from oracle and from how callers reference the type
// (`service.call(req)`), inflating FPs/FNs by pure rendering. The 2026-05-02
// plateau-diagnose Step 6 wide sample on psm assetman
// found 6% of method-residual was this exact rendering disagreement
// (Pattern E, "CALLER_GENERIC"). Stripping generics here makes the canonical
// QN match oracle's syn-extracted form.
static void rust_strip_generic_suffix(char* type_name) {
    if (!type_name) return;
    char* p = type_name;
    // Walk to the first `<` (if any) at the top level.
    while (*p && *p != '<') p++;
    if (*p != '<') return;
    // Trim any trailing whitespace before the `<`.
    while (p > type_name && (p[-1] == ' ' || p[-1] == '\t')) p--;
    *p = '\0';
}

static void extract_rust_impl(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec) {
    CBMArena* a = ctx->arena;

    TSNode type_node = ts_node_child_by_field_name(node, "type", 4);
    if (ts_node_is_null(type_node)) return;

    char* type_name = cbm_node_text(a, type_node, ctx->source);
    if (!type_name || !type_name[0]) return;
    rust_strip_generic_suffix(type_name);
    if (!type_name[0]) return;

    // Check for "impl Trait for Struct" pattern
    TSNode trait_node = ts_node_child_by_field_name(node, "trait", 5);
    if (!ts_node_is_null(trait_node)) {
        char* trait_name = cbm_node_text(a, trait_node, ctx->source);
        if (trait_name && trait_name[0]) {
            // Strip `<...>` from trait names too, e.g. `Service<ServiceRequest>`
            // -> `Service`. Matches the rendering used by oracle and callers.
            rust_strip_generic_suffix(trait_name);
            if (trait_name[0]) {
                CBMImplTrait it;
                it.trait_name = trait_name;
                it.struct_name = type_name;
                cbm_impltrait_push(&ctx->result->impl_traits, a, it);
            }
        }
    }

    const char* type_qn = cbm_fqn_compute(a, ctx->project, ctx->rel_path, type_name);

    // Extract methods inside impl body
    TSNode body = ts_node_child_by_field_name(node, "body", 4);
    if (ts_node_is_null(body)) return;

    uint32_t count = ts_node_child_count(body);
    for (uint32_t i = 0; i < count; i++) {
        TSNode child = ts_node_child(body, i);
        if (ts_node_is_null(child)) continue;
        if (!cbm_kind_in_set(child, spec->function_node_types)) continue;

        TSNode name_node = func_name_node(child);
        if (ts_node_is_null(name_node)) continue;

        char* name = cbm_node_text(a, name_node, ctx->source);
        if (!name || !name[0]) continue;

        const char* method_qn = cbm_arena_sprintf(a, "%s.%s", type_qn, name);

        CBMDefinition def;
        memset(&def, 0, sizeof(def));
        def.name = name;
        def.qualified_name = method_qn;
        def.label = "Method";
        def.file_path = ctx->rel_path;
        def.parent_class = type_qn;
        def.start_line = ts_node_start_point(child).row + 1;
        def.end_line = ts_node_end_point(child).row + 1;
        def.is_exported = cbm_is_exported(name, ctx->language);

        TSNode params = ts_node_child_by_field_name(child, "parameters", 10);
        if (!ts_node_is_null(params)) {
            def.signature = cbm_node_text(a, params, ctx->source);
            def.param_types = extract_param_types(a, params, ctx->source, ctx->language);
        }

        if (spec->branching_node_types && spec->branching_node_types[0]) {
            def.complexity = cbm_count_branching(child, spec->branching_node_types);
        }

        cbm_defs_push(&ctx->result->defs, a, def);
    }
}

// --- Variable extraction ---

// Helper to push a Variable definition
static void push_var_def(CBMExtractCtx* ctx, const char* name, TSNode node) {
    if (!name || !name[0] || strcmp(name, "_") == 0) return;
    CBMArena* a = ctx->arena;
    CBMDefinition def;
    memset(&def, 0, sizeof(def));
    def.name = name;
    def.qualified_name = cbm_fqn_compute(a, ctx->project, ctx->rel_path, name);
    def.label = "Variable";
    def.file_path = ctx->rel_path;
    def.start_line = ts_node_start_point(node).row + 1;
    def.end_line = ts_node_end_point(node).row + 1;
    def.is_exported = cbm_is_exported(name, ctx->language);
    cbm_defs_push(&ctx->result->defs, a, def);
}

// Helper: extract name from a declarator chain (C/C++/ObjC)
// declaration > init_declarator > declarator (may be pointer_declarator > identifier)
static const char* extract_c_declarator_name(CBMArena* a, TSNode decl, const char* source) {
    // Try "declarator" field on the declaration
    TSNode declarator = ts_node_child_by_field_name(decl, "declarator", 10);
    if (ts_node_is_null(declarator)) return NULL;

    // Could be init_declarator wrapping the actual declarator
    const char* dk = ts_node_type(declarator);
    if (strcmp(dk, "init_declarator") == 0) {
        declarator = ts_node_child_by_field_name(declarator, "declarator", 10);
        if (ts_node_is_null(declarator)) return NULL;
        dk = ts_node_type(declarator);
    }
    // Unwrap pointer_declarator
    while (strcmp(dk, "pointer_declarator") == 0 || strcmp(dk, "reference_declarator") == 0) {
        declarator = ts_node_child_by_field_name(declarator, "declarator", 10);
        if (ts_node_is_null(declarator)) return NULL;
        dk = ts_node_type(declarator);
    }
    if (strcmp(dk, "identifier") == 0) {
        return cbm_node_text(a, declarator, source);
    }
    return NULL;
}

// Helper: extract name from Java/C# field_declaration (declarator > name)
static const char* extract_java_field_name(CBMArena* a, TSNode field, const char* source) {
    TSNode declarator = ts_node_child_by_field_name(field, "declarator", 10);
    if (ts_node_is_null(declarator)) {
        // Try iterating children for variable_declarator
        uint32_t n = ts_node_named_child_count(field);
        for (uint32_t i = 0; i < n; i++) {
            TSNode child = ts_node_named_child(field, i);
            if (strcmp(ts_node_type(child), "variable_declarator") == 0) {
                declarator = child;
                break;
            }
        }
    }
    if (ts_node_is_null(declarator)) return NULL;
    TSNode name = ts_node_child_by_field_name(declarator, "name", 4);
    if (!ts_node_is_null(name)) return cbm_node_text(a, name, source);
    return NULL;
}

static void extract_var_names(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec) {
    CBMArena* a = ctx->arena;

    switch (ctx->language) {
        case CBM_LANG_PYTHON: {
            // assignment/augmented_assignment: left = right
            TSNode left = ts_node_child_by_field_name(node, "left", 4);
            if (!ts_node_is_null(left) && strcmp(ts_node_type(left), "identifier") == 0) {
                push_var_def(ctx, cbm_node_text(a, left, ctx->source), node);
            }
            break;
        }
        case CBM_LANG_GO: {
            // var_declaration -> var_spec* -> name
            // const_declaration -> const_spec* -> name
            uint32_t n = ts_node_named_child_count(node);
            for (uint32_t i = 0; i < n; i++) {
                TSNode child = ts_node_named_child(node, i);
                const char* ck = ts_node_type(child);
                if (strcmp(ck, "var_spec") == 0 || strcmp(ck, "const_spec") == 0) {
                    TSNode vname = ts_node_child_by_field_name(child, "name", 4);
                    if (!ts_node_is_null(vname)) {
                        push_var_def(ctx, cbm_node_text(a, vname, ctx->source), child);
                    }
                }
            }
            break;
        }
        case CBM_LANG_JAVASCRIPT:
        case CBM_LANG_TYPESCRIPT:
        case CBM_LANG_TSX: {
            // lexical_declaration/variable_declaration -> variable_declarator -> name
            // Skip if value is arrow_function or function_expression (extracted as Function)
            uint32_t n = ts_node_named_child_count(node);
            for (uint32_t i = 0; i < n; i++) {
                TSNode child = ts_node_named_child(node, i);
                if (strcmp(ts_node_type(child), "variable_declarator") != 0) continue;
                // Skip function-valued variables
                TSNode value = ts_node_child_by_field_name(child, "value", 5);
                if (!ts_node_is_null(value)) {
                    const char* vk = ts_node_type(value);
                    if (strcmp(vk, "arrow_function") == 0 || strcmp(vk, "function_expression") == 0 ||
                        strcmp(vk, "generator_function") == 0) {
                        continue;
                    }
                }
                TSNode vname = ts_node_child_by_field_name(child, "name", 4);
                if (!ts_node_is_null(vname)) {
                    push_var_def(ctx, cbm_node_text(a, vname, ctx->source), child);
                }
            }
            break;
        }
        case CBM_LANG_JAVA: {
            // field_declaration -> variable_declarator -> name
            const char* fname = extract_java_field_name(a, node, ctx->source);
            if (fname) push_var_def(ctx, fname, node);
            break;
        }
        case CBM_LANG_CPP:
        case CBM_LANG_C: {
            // declaration > init_declarator > declarator chain > identifier
            const char* vname = extract_c_declarator_name(a, node, ctx->source);
            if (vname) push_var_def(ctx, vname, node);
            break;
        }
        case CBM_LANG_RUST: {
            // static_item/const_item: name field
            TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
            if (!ts_node_is_null(name_node)) {
                push_var_def(ctx, cbm_node_text(a, name_node, ctx->source), node);
            }
            break;
        }
        case CBM_LANG_YAML: {
            // block_mapping_pair: key field
            TSNode key = ts_node_child_by_field_name(node, "key", 3);
            if (!ts_node_is_null(key)) {
                push_var_def(ctx, cbm_node_text(a, key, ctx->source), node);
            }
            break;
        }
        case CBM_LANG_TOML: {
            // pair: first child is bare_key/dotted_key/quoted_key
            uint32_t nc = ts_node_child_count(node);
            for (uint32_t i = 0; i < nc; i++) {
                TSNode child = ts_node_child(node, i);
                const char* ck = ts_node_type(child);
                if (strcmp(ck, "bare_key") == 0 || strcmp(ck, "dotted_key") == 0 ||
                    strcmp(ck, "quoted_key") == 0 || strcmp(ck, "key") == 0) {
                    push_var_def(ctx, cbm_node_text(a, child, ctx->source), node);
                    break;
                }
            }
            break;
        }
        case CBM_LANG_JSON: {
            // pair: "key" field is a string node, extract unquoted text
            TSNode key_node = ts_node_child_by_field_name(node, "key", 3);
            if (!ts_node_is_null(key_node)) {
                char* raw = cbm_node_text(a, key_node, ctx->source);
                if (raw) {
                    // Strip surrounding quotes if present
                    size_t rlen = strlen(raw);
                    if (rlen >= 2 && raw[0] == '"' && raw[rlen-1] == '"') {
                        raw[rlen-1] = '\0';
                        raw++;
                    }
                    push_var_def(ctx, raw, node);
                }
            }
            break;
        }
        case CBM_LANG_SQL: {
            // create_table/create_view: find identifier/object_reference
            uint32_t n = ts_node_named_child_count(node);
            for (uint32_t i = 0; i < n; i++) {
                TSNode child = ts_node_named_child(node, i);
                const char* ck = ts_node_type(child);
                if (strcmp(ck, "identifier") == 0 || strcmp(ck, "object_reference") == 0) {
                    push_var_def(ctx, cbm_node_text(a, child, ctx->source), node);
                    break;
                }
            }
            break;
        }
        case CBM_LANG_BASH: {
            // variable_assignment: name = value
            TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
            if (!ts_node_is_null(name_node)) {
                push_var_def(ctx, cbm_node_text(a, name_node, ctx->source), node);
            } else {
                // Fallback: first word before =
                uint32_t n = ts_node_named_child_count(node);
                for (uint32_t i = 0; i < n; i++) {
                    TSNode child = ts_node_named_child(node, i);
                    const char* ck = ts_node_type(child);
                    if (strcmp(ck, "variable_name") == 0 || strcmp(ck, "word") == 0) {
                        push_var_def(ctx, cbm_node_text(a, child, ctx->source), node);
                        break;
                    }
                }
            }
            break;
        }
        case CBM_LANG_SCSS: {
            // declaration: property_name child (SCSS variable like $primary-color: value)
            TSNode prop = ts_node_child_by_field_name(node, "property", 8);
            if (ts_node_is_null(prop)) prop = ts_node_child_by_field_name(node, "name", 4);
            if (ts_node_is_null(prop)) prop = cbm_find_child_by_kind(node, "property_name");
            if (ts_node_is_null(prop)) prop = cbm_find_child_by_kind(node, "variable_name");
            if (!ts_node_is_null(prop)) {
                push_var_def(ctx, cbm_node_text(a, prop, ctx->source), node);
            }
            break;
        }
        default: {
            // Try "name" field first, then C-style declarator chain, then first identifier
            TSNode name_node = ts_node_child_by_field_name(node, "name", 4);
            if (!ts_node_is_null(name_node)) {
                push_var_def(ctx, cbm_node_text(a, name_node, ctx->source), node);
                break;
            }
            // Try C-style declarator chain
            const char* cname = extract_c_declarator_name(a, node, ctx->source);
            if (cname) { push_var_def(ctx, cname, node); break; }
            // Generic: try first identifier child
            uint32_t n = ts_node_named_child_count(node);
            for (uint32_t i = 0; i < n; i++) {
                TSNode child = ts_node_named_child(node, i);
                if (strcmp(ts_node_type(child), "identifier") == 0) {
                    push_var_def(ctx, cbm_node_text(a, child, ctx->source), node);
                    break;
                }
            }
            break;
        }
    }
}

// Recursive variable walker for languages with deeply nested module structure.
// Used by YAML, TOML, INI, JSON (config languages with nested containers).
static void walk_variables_rec(CBMExtractCtx* ctx, TSNode node, const CBMLangSpec* spec, int depth) {
    if (depth > 8) return; // safety limit
    uint32_t count = ts_node_child_count(node);
    for (uint32_t i = 0; i < count; i++) {
        TSNode child = ts_node_child(node, i);
        if (ts_node_is_null(child)) continue;
        if (cbm_kind_in_set(child, spec->variable_node_types)) {
            if (cbm_is_module_level(child, ctx->language)) {
                extract_var_names(ctx, child, spec);
            }
        }
        // Always recurse into structural container nodes
        const char* ck = ts_node_type(child);
        if (strcmp(ck, "document") == 0 || strcmp(ck, "block_node") == 0 ||
            strcmp(ck, "block_mapping") == 0 || strcmp(ck, "stream") == 0 ||
            // TOML containers
            strcmp(ck, "table") == 0 || strcmp(ck, "table_array_element") == 0 ||
            // INI containers
            strcmp(ck, "section") == 0 ||
            // JSON/TOML containers (pair can contain nested objects)
            strcmp(ck, "object") == 0 || strcmp(ck, "array") == 0 ||
            strcmp(ck, "pair") == 0 ||
            // XML containers
            strcmp(ck, "element") == 0 || strcmp(ck, "content") == 0) {
            walk_variables_rec(ctx, child, spec, depth + 1);
        }
    }
}

// Nix-specific: walk the AST emitting Option-labeled definitions for
// `name = mkOption { ... };` bindings inside NixOS modules. The 2026-05-13
// PSM tool-comparison battery surfaced that NixOS option declarations
// (100+ on PSM in nix/modules/) were invisible to code-graph — Nix
// bindings weren't extracted via the standard variable path, and even
// when they were, mkOption-shaped bindings need a distinct label
// because they're a different conceptual node (config option, not
// a regular variable).
//
// AST shape (tree-sitter Nix):
//   binding
//     attrpath: (attrpath (identifier "enable"))
//     "="
//     expression: (apply_expression
//       function: (variable_expression (identifier "mkOption"))
//       argument: (attrset_expression { ... }))
//     ";"
static int nix_apply_expression_is_mkoption(TSNode expr, const char* source) {
    if (ts_node_is_null(expr)) return 0;
    if (strcmp(ts_node_type(expr), "apply_expression") != 0) return 0;
    TSNode func = ts_node_child_by_field_name(expr, "function", 8);
    if (ts_node_is_null(func)) {
        uint32_t nc = ts_node_named_child_count(expr);
        if (nc > 0) func = ts_node_named_child(expr, 0);
    }
    if (ts_node_is_null(func)) return 0;
    // The function may be bare `mkOption` or qualified `lib.mkOption`.
    // Walk down to the rightmost identifier and compare its text.
    for (int safety = 0; safety < 10; safety++) {
        const char* fk = ts_node_type(func);
        if (strcmp(fk, "variable_expression") == 0) {
            uint32_t nc = ts_node_named_child_count(func);
            if (nc > 0) {
                func = ts_node_named_child(func, 0);
                continue;
            }
        }
        if (strcmp(fk, "select_expression") == 0) {
            uint32_t nc = ts_node_named_child_count(func);
            if (nc > 0) {
                func = ts_node_named_child(func, nc - 1);
                continue;
            }
        }
        if (strcmp(fk, "attrpath") == 0) {
            uint32_t nc = ts_node_named_child_count(func);
            if (nc > 0) {
                func = ts_node_named_child(func, nc - 1);
                continue;
            }
        }
        break;
    }
    const char* fk = ts_node_type(func);
    if (strcmp(fk, "identifier") != 0) return 0;
    uint32_t start = ts_node_start_byte(func);
    uint32_t end = ts_node_end_byte(func);
    size_t len = (size_t)(end - start);
    if (len == 8 && strncmp(source + start, "mkOption", 8) == 0) return 1;
    return 0;
}

static char* nix_binding_attrpath_name(CBMArena* a, TSNode binding, const char* source) {
    TSNode attrpath = ts_node_child_by_field_name(binding, "attrpath", 8);
    if (ts_node_is_null(attrpath)) {
        uint32_t nc = ts_node_named_child_count(binding);
        for (uint32_t i = 0; i < nc; i++) {
            TSNode c = ts_node_named_child(binding, i);
            if (strcmp(ts_node_type(c), "attrpath") == 0) {
                attrpath = c;
                break;
            }
        }
    }
    if (ts_node_is_null(attrpath)) return NULL;
    // attrpath holds 1+ identifier nodes; for dotted paths like
    // `services.foo.enable`, the last identifier is the leaf option
    // name (mirrors NixOS option-tree convention).
    uint32_t nc = ts_node_named_child_count(attrpath);
    TSNode last_id = {0};
    int found = 0;
    for (uint32_t i = 0; i < nc; i++) {
        TSNode c = ts_node_named_child(attrpath, i);
        if (strcmp(ts_node_type(c), "identifier") == 0) {
            last_id = c;
            found = 1;
        }
    }
    if (!found) return NULL;
    return cbm_node_text(a, last_id, source);
}

static void walk_nix_mkoption_bindings(CBMExtractCtx* ctx, TSNode node, int depth) {
    if (depth > 50) return;
    const char* kind = ts_node_type(node);
    if (strcmp(kind, "binding") == 0) {
        TSNode expr = ts_node_child_by_field_name(node, "expression", 10);
        if (ts_node_is_null(expr)) {
            // Fallback: find the last non-attrpath named child.
            uint32_t nc = ts_node_named_child_count(node);
            for (uint32_t i = 0; i < nc; i++) {
                TSNode c = ts_node_named_child(node, i);
                if (strcmp(ts_node_type(c), "attrpath") != 0) {
                    expr = c;
                }
            }
        }
        if (!ts_node_is_null(expr) && nix_apply_expression_is_mkoption(expr, ctx->source)) {
            char* opt_name = nix_binding_attrpath_name(ctx->arena, node, ctx->source);
            if (opt_name && opt_name[0]) {
                CBMDefinition def;
                memset(&def, 0, sizeof(def));
                def.name = opt_name;
                def.qualified_name = cbm_fqn_compute(
                    ctx->arena, ctx->project, ctx->rel_path, opt_name);
                def.label = "Option";
                def.file_path = ctx->rel_path;
                def.start_line = ts_node_start_point(node).row + 1;
                def.end_line = ts_node_end_point(node).row + 1;
                def.is_exported = true;
                const char** decorators = (const char**)cbm_arena_alloc(
                    ctx->arena, 2 * sizeof(const char*));
                if (decorators) {
                    decorators[0] = "mkOption";
                    decorators[1] = NULL;
                    def.decorators = decorators;
                }
                cbm_defs_push(&ctx->result->defs, ctx->arena, def);
            }
        }
        // Continue recursion — submodule { options = {...}; } may have
        // nested mkOption bindings within the attrset_expression.
    }
    uint32_t n = ts_node_child_count(node);
    for (uint32_t i = 0; i < n; i++) {
        TSNode child = ts_node_child(node, i);
        if (ts_node_is_null(child)) continue;
        walk_nix_mkoption_bindings(ctx, child, depth + 1);
    }
}

// Rust-specific: walk the AST emitting Variable definitions for `defvar!`
// macro invocations. PSM's defvar! macro declares a typed env-var-backed
// constant; the 2026-05-13 PSM tool-comparison battery surfaced that
// these were invisible to code-graph because macro_invocation nodes are
// extracted only as CALLS edges, not as Definition nodes. With this
// pass, `defvar!(NAME: T = default, or try ...)` emits a Variable named
// `NAME` with file_path/start_line/decorators=["defvar"].
//
// AST shape (tree-sitter Rust):
//   (macro_invocation
//     macro: (identifier "defvar")
//     bang: "!"
//     arguments: (token_tree
//       "("
//       (identifier "NAME")        <- what we extract
//       ":" (type ...) "=" (expr ...) ","
//       ... rest of token_tree
//       ")")
//   )
//
// Falls back to iterating named children when the `macro` /
// `arguments` field-name lookup returns null on this grammar version.
static void walk_rust_defvar_macros(CBMExtractCtx* ctx, TSNode node, int depth) {
    if (depth > 50) return;  // safety bound
    const char* kind = ts_node_type(node);
    if (strcmp(kind, "macro_invocation") == 0) {
        TSNode macro_name = ts_node_child_by_field_name(node, "macro", 5);
        if (ts_node_is_null(macro_name)) {
            // Fallback: first named child is typically the identifier
            uint32_t nc = ts_node_named_child_count(node);
            if (nc > 0) {
                macro_name = ts_node_named_child(node, 0);
            }
        }
        if (!ts_node_is_null(macro_name)) {
            const char* mn_kind = ts_node_type(macro_name);
            // Accept identifier or scoped_identifier (e.g. some_crate::defvar)
            if (strcmp(mn_kind, "identifier") == 0 ||
                strcmp(mn_kind, "scoped_identifier") == 0) {
                char* mn_text = cbm_node_text(ctx->arena, macro_name, ctx->source);
                // Check if the name is "defvar" or ends with "::defvar".
                int is_defvar = 0;
                if (mn_text && mn_text[0]) {
                    size_t len = strlen(mn_text);
                    if (len == 6 && strcmp(mn_text, "defvar") == 0) {
                        is_defvar = 1;
                    } else if (len > 6 &&
                               strcmp(mn_text + len - 6, "defvar") == 0 &&
                               mn_text[len - 7] == ':') {
                        is_defvar = 1;
                    }
                }
                if (is_defvar) {
                    // Locate token_tree (arguments field). First identifier
                    // inside is the variable name.
                    TSNode args = ts_node_child_by_field_name(node, "arguments", 9);
                    if (ts_node_is_null(args)) {
                        uint32_t nc = ts_node_named_child_count(node);
                        for (uint32_t i = 0; i < nc; i++) {
                            TSNode c = ts_node_named_child(node, i);
                            if (strcmp(ts_node_type(c), "token_tree") == 0) {
                                args = c;
                                break;
                            }
                        }
                    }
                    if (!ts_node_is_null(args)) {
                        uint32_t ac = ts_node_child_count(args);
                        for (uint32_t i = 0; i < ac; i++) {
                            TSNode c = ts_node_child(args, i);
                            if (strcmp(ts_node_type(c), "identifier") == 0) {
                                char* var_name = cbm_node_text(ctx->arena, c, ctx->source);
                                if (var_name && var_name[0] && strcmp(var_name, "_") != 0) {
                                    CBMDefinition def;
                                    memset(&def, 0, sizeof(def));
                                    def.name = var_name;
                                    def.qualified_name = cbm_fqn_compute(
                                        ctx->arena, ctx->project, ctx->rel_path, var_name);
                                    def.label = "Variable";
                                    def.file_path = ctx->rel_path;
                                    def.start_line = ts_node_start_point(node).row + 1;
                                    def.end_line = ts_node_end_point(node).row + 1;
                                    def.is_exported = cbm_is_exported(var_name, ctx->language);
                                    // Decorators array: ["defvar", NULL].
                                    // Tag lets callers query for defvar-emitted
                                    // Variables without changing the label.
                                    const char** decorators = (const char**)cbm_arena_alloc(
                                        ctx->arena, 2 * sizeof(const char*));
                                    if (decorators) {
                                        decorators[0] = "defvar";
                                        decorators[1] = NULL;
                                        def.decorators = decorators;
                                    }
                                    cbm_defs_push(&ctx->result->defs, ctx->arena, def);
                                }
                                break;
                            }
                        }
                    }
                }
            }
        }
        // Don't recurse INTO the macro_invocation token_tree — its contents
        // are token-list tokens, not legitimate macro_invocation nodes we
        // want to extract from.
        return;
    }
    uint32_t n = ts_node_child_count(node);
    for (uint32_t i = 0; i < n; i++) {
        TSNode child = ts_node_child(node, i);
        if (ts_node_is_null(child)) continue;
        walk_rust_defvar_macros(ctx, child, depth + 1);
    }
}

static void extract_variables(CBMExtractCtx* ctx, TSNode root, const CBMLangSpec* spec) {
    if (!spec->variable_node_types || !spec->variable_node_types[0]) return;

    // Config languages with nested structure: use recursive walk
    if (ctx->language == CBM_LANG_YAML || ctx->language == CBM_LANG_TOML ||
        ctx->language == CBM_LANG_JSON) {
        walk_variables_rec(ctx, root, spec, 0);
        return;
    }

    // Rust: walk for defvar! macro invocations BEFORE the static/const pass.
    // PSM has 50+ sites where defvar! declares env-var-backed configuration
    // constants invisible to the standard variable extractor. See
    // walk_rust_defvar_macros above for AST shape + rationale.
    if (ctx->language == CBM_LANG_RUST) {
        walk_rust_defvar_macros(ctx, root, 0);
        // Fall through to the static/const pass below.
    }

    // Nix: walk for `name = mkOption {...};` bindings. PSM has 100+ such
    // bindings in nix/modules/; they declare NixOS module options and
    // are conceptually distinct from regular bindings (different label).
    // See walk_nix_mkoption_bindings above for AST shape + rationale.
    if (ctx->language == CBM_LANG_NIX) {
        walk_nix_mkoption_bindings(ctx, root, 0);
        // Fall through — the existing binding pass (if reached) emits
        // Variable defs for non-mkOption bindings via the default case
        // in extract_var_names.
    }

    uint32_t count = ts_node_child_count(root);
    for (uint32_t i = 0; i < count; i++) {
        TSNode child = ts_node_child(root, i);
        if (ts_node_is_null(child)) continue;

        if (!cbm_is_module_level(child, ctx->language)) continue;

        if (cbm_kind_in_set(child, spec->variable_node_types)) {
            extract_var_names(ctx, child, spec);
            continue;
        }

        // Unwrap wrapper nodes: expression_statement, export_statement, statement
        const char* ck = ts_node_type(child);
        if (strcmp(ck, "expression_statement") == 0 || strcmp(ck, "export_statement") == 0 ||
            strcmp(ck, "statement") == 0) {
            // Check inner named children for variable types
            uint32_t nc = ts_node_named_child_count(child);
            for (uint32_t j = 0; j < nc; j++) {
                TSNode inner = ts_node_named_child(child, j);
                if (cbm_kind_in_set(inner, spec->variable_node_types)) {
                    extract_var_names(ctx, inner, spec);
                }
            }
            // Also check if the wrapper itself is a variable type (e.g., PHP expression_statement)
            if (cbm_kind_in_set(child, spec->variable_node_types)) {
                extract_var_names(ctx, child, spec);
            }
        }
    }
}

// Extract class-level variables (field declarations inside class bodies)
static void extract_class_variables(CBMExtractCtx* ctx, TSNode class_node, const CBMLangSpec* spec) {
    if (!spec->variable_node_types || !spec->variable_node_types[0]) return;

    TSNode body = find_class_body(class_node, ctx->language);
    if (ts_node_is_null(body)) return;

    uint32_t count = ts_node_named_child_count(body);
    for (uint32_t i = 0; i < count; i++) {
        TSNode child = ts_node_named_child(body, i);
        if (cbm_kind_in_set(child, spec->variable_node_types)) {
            extract_var_names(ctx, child, spec);
        }
    }
}

// --- Module node + main walk ---

// Definition walker. Iterative with a growable heap frame stack rather than
// recursion, so AST depth never touches the C stack and a file with thousands
// of top-level definitions is fully extracted. Ported from upstream
// codebase-memory-mcp 174e56b4 (#668): the fixed 4096-frame array both
// overflowed small thread stacks and silently dropped definitions past 4096.
// Growth is bounded by CBM_WALK_DEFS_MAX (default 8M frames); past it the walk
// stops descending and flags the result as depth_capped instead of dropping
// silently.
typedef struct {
    TSNode* data;
    int     top;
    int     cap;
    bool    capped;
} wd_stack_t;

static int wd_stack_max(void) {
    static int cached = 0;
    if (cached == 0) {
        int v = 8 * 1024 * 1024;
        const char* e = getenv("CBM_WALK_DEFS_MAX");
        if (e && *e) {
            int parsed = atoi(e);
            if (parsed > 0) v = parsed;
        }
        cached = v;
    }
    return cached;
}

static bool wd_push(wd_stack_t* s, TSNode node) {
    if (s->top >= s->cap) {
        int ncap = s->cap ? s->cap * 2 : 256;
        if (ncap > wd_stack_max()) {
            s->capped = true;
            return false;
        }
        TSNode* nd = (TSNode*)realloc(s->data, (size_t)ncap * sizeof(TSNode));
        if (!nd) {
            // OOM: drain what we have and stop; extraction keeps what was emitted.
            free(s->data);
            s->data = NULL;
            s->cap = 0;
            s->top = 0;
            s->capped = true;
            return false;
        }
        s->data = nd;
        s->cap = ncap;
    }
    s->data[s->top++] = node;
    return true;
}

// Push all children so they pop in source order. One O(N) cursor pass, then
// reverse the pushed segment; ts_node_child(node, i) is O(i), so the naive
// reverse loop is O(N^2) on wide roots.
static void wd_push_children(wd_stack_t* s, TSNode node) {
    int base = s->top;
    TSTreeCursor cursor = ts_tree_cursor_new(node);
    if (ts_tree_cursor_goto_first_child(&cursor)) {
        do {
            if (!wd_push(s, ts_tree_cursor_current_node(&cursor))) break;
        } while (ts_tree_cursor_goto_next_sibling(&cursor));
    }
    ts_tree_cursor_delete(&cursor);
    int lo = base, hi = s->top - 1;
    while (lo < hi) {
        TSNode tmp = s->data[lo];
        s->data[lo] = s->data[hi];
        s->data[hi] = tmp;
        lo++;
        hi--;
    }
}

static void walk_defs(CBMExtractCtx* ctx, TSNode root, const CBMLangSpec* spec) {
    wd_stack_t s = {0};
    wd_push(&s, root);
    while (s.top > 0) {
        TSNode node = s.data[--s.top];
        const char* kind = ts_node_type(node);

        // Function types
        if (cbm_kind_in_set(node, spec->function_node_types)) {
            extract_func_def(ctx, node, spec);
            continue; // don't descend into function bodies for nested defs
        }

        // Rust impl blocks
        if (ctx->language == CBM_LANG_RUST && strcmp(kind, "impl_item") == 0) {
            extract_rust_impl(ctx, node, spec);
            continue;
        }

        // Class types
        if (cbm_kind_in_set(node, spec->class_node_types)) {
            extract_class_def(ctx, node, spec);
            // Config languages have nested classes (XML elements, TOML tables)
            // — keep descending instead of stopping
            if (ctx->language == CBM_LANG_XML || ctx->language == CBM_LANG_TOML ||
                ctx->language == CBM_LANG_MARKDOWN) {
                wd_push_children(&s, node);
            }
            continue;
        }

        wd_push_children(&s, node);
    }
    if (s.capped) ctx->walk_depth_capped = true;
    free(s.data);
}

void cbm_extract_definitions(CBMExtractCtx* ctx) {
    const CBMLangSpec* spec = cbm_lang_spec(ctx->language);
    if (!spec) return;

    CBMArena* a = ctx->arena;

    // Create module node (always first definition)
    CBMDefinition mod;
    memset(&mod, 0, sizeof(mod));
    mod.name = ctx->rel_path;  // will be refined by Go layer
    mod.qualified_name = ctx->module_qn;
    mod.label = "Module";
    mod.file_path = ctx->rel_path;
    mod.start_line = 1;
    mod.end_line = ts_node_end_point(ctx->root).row + 1;
    mod.is_exported = true;
    mod.is_test = ctx->result->is_test_file;
    cbm_defs_push(&ctx->result->defs, a, mod);

    // Walk AST for function/class definitions
    walk_defs(ctx, ctx->root, spec);

    // Extract module-level variables
    extract_variables(ctx, ctx->root, spec);
}
