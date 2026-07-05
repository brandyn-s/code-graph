package store

// IsSurfaceableCodeNode reports whether a node represents concrete, openable
// source code — i.e. a legitimate result for the localization, ranking, and
// security-surface tools. It excludes two node classes that otherwise
// pollute result lists with entries the caller cannot act on:
//
//   - Community pseudo-nodes: Louvain cluster aggregates (label "Community",
//     no file). BFS over MEMBER_OF edges reaches them, and rank/localize
//     surfaced them directly (observed live 2026-07-04: code_localize
//     returned a "Getwd_cluster" Community node).
//
//   - External-dependency stubs: CALLS_EXTERNAL targets carry Function/Method
//     labels but have no file_path (e.g. os.WriteFile,
//     github.com/DeusData/.../Executor.Execute). They are not code in THIS
//     repo, so they cannot be opened or investigated.
//
// A legitimate first-party symbol always has a non-empty file_path, so the
// empty-file_path check captures external stubs, and the label check
// captures Community aggregates (which also happen to have no file). Module
// / File nodes with real paths are retained — they are surfaceable.
func IsSurfaceableCodeNode(label, filePath string) bool {
	return filePath != "" && label != "Community"
}
