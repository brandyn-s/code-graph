package store

import "sort"

// Edge type constants. Every relationship kind the graph can hold is declared
// here once; passes emit edges with these constants, and EdgeTypes documents
// each one. A test in this package fails when production code introduces a
// new `Type: "LITERAL"` outside this table, so the schema stays enumerable
// for get_graph_schema, docs/edge-types.md, and downstream consumers.
const (
	// Structure.
	EdgeContains     = "CONTAINS"
	EdgeContainsFile = "CONTAINS_FILE"
	EdgeDefines      = "DEFINES"
	EdgeDefinesMeth  = "DEFINES_METHOD"
	EdgeDefinesField = "DEFINES_FIELD"
	EdgeMemberOf     = "MEMBER_OF"
	EdgeParameterOf  = "PARAMETER_OF"

	// Calls family.
	EdgeCalls         = "CALLS"
	EdgeCallReference = "CALL_REFERENCE"
	EdgeCallsExternal = "CALLS_EXTERNAL"
	EdgeCallsPseudo   = "CALLS_PSEUDO"
	EdgeIndirectCalls = "INDIRECT_CALLS"
	EdgeHTTPCalls     = "HTTP_CALLS"
	EdgeAsyncCalls    = "ASYNC_CALLS"
	EdgeHandles       = "HANDLES"

	// Types and inheritance.
	EdgeImports    = "IMPORTS"
	EdgeImplements = "IMPLEMENTS"
	EdgeInherits   = "INHERITS"
	EdgeOverride   = "OVERRIDE"
	EdgeUsesType   = "USES_TYPE"
	EdgeUsage      = "USAGE"
	EdgeDecorates  = "DECORATES"

	// Data flow and errors.
	EdgeReads  = "READS"
	EdgeWrites = "WRITES"
	EdgeThrows = "THROWS"
	EdgeRaises = "RAISES"

	// Tests.
	EdgeTests     = "TESTS"
	EdgeTestsFile = "TESTS_FILE"

	// Configuration, infrastructure, and policy.
	EdgeConfigures  = "CONFIGURES"
	EdgeDependsOn   = "DEPENDS_ON"
	EdgeReadsEnv    = "READS_ENV"
	EdgePolicyGates = "POLICY_GATES"
	EdgeRunsBinary  = "RUNS_BINARY"

	// Messaging.
	EdgePublishesTo  = "PUBLISHES_TO"
	EdgeSubscribesTo = "SUBSCRIBES_TO"
	EdgeQueries      = "QUERIES"
	EdgeAnswers      = "ANSWERS"

	// Derived.
	EdgeFileChangesWith       = "FILE_CHANGES_WITH"
	EdgeSemanticallySimilarTo = "SEMANTICALLY_SIMILAR_TO"
	EdgeRationaleFor          = "RATIONALE_FOR"
)

// EdgeTypeInfo documents one edge type.
type EdgeTypeInfo struct {
	Type string
	// Family groups related kinds for filtering: structure, calls, types,
	// dataflow, tests, config, messaging, derived.
	Family string
	// Source and Target name the node roles in "source -> target" order.
	Source, Target string
	Doc            string
}

// EdgeTypes is the documented table of every edge kind, sorted by type.
var EdgeTypes = func() []EdgeTypeInfo {
	t := []EdgeTypeInfo{
		{EdgeContains, "structure", "Directory or Package", "Directory, Package, or File", "Filesystem and package containment produced by the structure pass."},
		{EdgeContainsFile, "structure", "Directory", "File", "Direct file containment used for fast per-directory listings."},
		{EdgeDefines, "structure", "File or Module", "Function, Class, or Variable", "A file or module defines a top-level symbol."},
		{EdgeDefinesMeth, "structure", "Class, Struct, or Trait", "Method", "A type defines a method."},
		{EdgeDefinesField, "structure", "Class or Struct", "Field", "A type defines a field."},
		{EdgeMemberOf, "structure", "Symbol", "Community", "Louvain community membership computed after indexing."},
		{EdgeParameterOf, "structure", "Parameter", "Function", "A parameter belongs to a function; used by data-flow reachability."},

		{EdgeCalls, "calls", "Function", "Function", "A resolved call. Properties carry resolver rule, strategy, confidence, and, for SCIP-derived edges, the artifact digest."},
		{EdgeCallsExternal, "calls", "Function", "External stub", "A call whose target lives outside the indexed repository."},
		{EdgeCallsPseudo, "calls", "Function", "Pseudo target", "A call to a language construct modelled as a pseudo node (modal dispatch, builtins)."},
		{EdgeIndirectCalls, "calls", "Function", "Function", "A call reached through a function value, callback, or dispatch table."},
		{EdgeHTTPCalls, "calls", "Function", "Route handler", "A cross-service HTTP call matched to the handler that serves the route."},
		{EdgeAsyncCalls, "calls", "Function", "Function", "A call across an async boundary (task spawn, message dispatch)."},
		{EdgeHandles, "calls", "Route", "Function", "A route node is served by a handler function."},
		{EdgeCallReference, "calls", "Function or Module", "Function or Method", "A callable referenced at a value site (assignment, collection literal, argument) that resolves to exactly one target; not an invocation. Aligned with upstream codebase-memory-mcp: CALL_REFERENCE is the proven-single-target counterpart of USAGE."},

		{EdgeImports, "types", "Module or File", "Module", "An import statement, normalized for relative imports."},
		{EdgeImplements, "types", "Type", "Interface or Trait", "A type implements an interface or trait."},
		{EdgeInherits, "types", "Class", "Class", "Class inheritance."},
		{EdgeOverride, "types", "Method", "Method", "A method overrides a parent or interface method."},
		{EdgeUsesType, "types", "Function or Field", "Type", "A signature or field references a type."},
		{EdgeUsage, "types", "Function or Module", "Variable, Constant, Type, or Function", "An identifier used at a value site where no unique callable target is proven (a non-callable symbol, or an ambiguous or fuzzy resolution). The unproven counterpart of CALL_REFERENCE."},
		{EdgeDecorates, "types", "Decorator", "Function or Class", "A decorator or attribute applied to a definition."},

		{EdgeReads, "dataflow", "Function", "Variable or Field", "A read of a variable or field."},
		{EdgeWrites, "dataflow", "Function", "Variable or Field", "A write to a variable or field."},
		{EdgeThrows, "dataflow", "Function", "Type", "A function throws an exception type (statically typed languages)."},
		{EdgeRaises, "dataflow", "Function", "Type", "A function raises an exception type (Python)."},

		{EdgeTests, "tests", "Test function", "Function", "A test exercises a production function."},
		{EdgeTestsFile, "tests", "Test file", "File", "A test file covers a production file by convention."},

		{EdgeConfigures, "config", "Config file or Service", "Service, Function, or Variable", "Configuration links a config artifact to the code it configures."},
		{EdgeDependsOn, "config", "Package or Service", "Package or Service", "A declared dependency from a lockfile, manifest, or infrastructure module."},
		{EdgeReadsEnv, "config", "Function", "EnvVar", "Code reads an environment variable."},
		{EdgePolicyGates, "config", "Policy", "Function or Route", "An OPA policy gates the target."},
		{EdgeRunsBinary, "config", "Service", "Binary", "A declared service runs a binary (Nix modules)."},

		{EdgePublishesTo, "messaging", "Service or Function", "Topic", "Publishes to a topic (Zenoh, Nix service declarations)."},
		{EdgeSubscribesTo, "messaging", "Service or Function", "Topic", "Subscribes to a topic."},
		{EdgeQueries, "messaging", "Function", "Topic", "Issues a query on a topic (Zenoh)."},
		{EdgeAnswers, "messaging", "Function", "Topic", "Answers queries on a topic (Zenoh queryable)."},

		{EdgeFileChangesWith, "derived", "File", "File", "Co-change coupling mined from git history."},
		{EdgeSemanticallySimilarTo, "derived", "Function", "Function", "Embedding cosine similarity above threshold (opt-in)."},
		{EdgeRationaleFor, "derived", "Rationale", "Function or Class", "A WHY/SAFETY/NOTE annotation explains the target."},
	}
	sort.Slice(t, func(i, j int) bool { return t[i].Type < t[j].Type })
	return t
}()

// KnownEdgeType reports whether t is a documented edge type.
func KnownEdgeType(t string) bool {
	for _, e := range EdgeTypes {
		if e.Type == t {
			return true
		}
	}
	return false
}
