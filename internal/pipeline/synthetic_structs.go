package pipeline

// SyntheticStructRegistry — curated map of well-known external struct/type
// names that PSM and similar Rust codebases use as the implementing-side
// type in `impl Trait for Struct` blocks but which aren't in the indexed
// graph as Class/Struct/Enum nodes (because std/external crates aren't
// indexed).
//
// Mirror of synthetic_traits.go (PR #266) for the struct side. After
// shipping PR #266+#267, PSM had 62 structQN-empty:resolve-empty cases
// remaining — these are foreign-impls on external types like
// `impl SomeTrait for Vec<MyType>` where Vec is the external struct.
//
// Same architecture: synthetic QN uses `_external.<crate>.<struct>`
// namespace prefix; on-demand node upsert in implements.go's
// struct-node-nil branch creates the synthetic Class node so emitImpl
// can wire the IMPLEMENTS edge.
//
// Substrate: 62+13 = 75 PSM cases (structQN-empty:resolve-empty +
// structQN-empty:label-mismatch combined).

// syntheticStructs maps struct/type NAMES (as seen in `impl Trait for X`)
// to synthetic Class QNs.
var syntheticStructs = map[string]string{
	// std collections
	"Vec":        "_external.std.vec.Vec",
	"VecDeque":   "_external.std.collections.VecDeque",
	"HashMap":    "_external.std.collections.HashMap",
	"BTreeMap":   "_external.std.collections.BTreeMap",
	"HashSet":    "_external.std.collections.HashSet",
	"BTreeSet":   "_external.std.collections.BTreeSet",
	"LinkedList": "_external.std.collections.LinkedList",
	"BinaryHeap": "_external.std.collections.BinaryHeap",
	// std smart pointers + interior mutability
	"Box":      "_external.std.boxed.Box",
	"Rc":       "_external.std.rc.Rc",
	"Arc":      "_external.std.sync.Arc",
	"Weak":     "_external.std.rc.Weak",
	"RefCell":  "_external.std.cell.RefCell",
	"Cell":     "_external.std.cell.Cell",
	"OnceCell": "_external.std.cell.OnceCell",
	"Mutex":    "_external.std.sync.Mutex",
	"RwLock":   "_external.std.sync.RwLock",
	// std options + misc
	"Option":     "_external.std.option.Option",
	"Result":     "_external.std.result.Result",
	"String":     "_external.std.string.String",
	"OsString":   "_external.std.ffi.OsString",
	"PathBuf":    "_external.std.path.PathBuf",
	"Path":       "_external.std.path.Path",
	"Duration":   "_external.std.time.Duration",
	"Instant":    "_external.std.time.Instant",
	"SystemTime": "_external.std.time.SystemTime",
	// std error
	"IoError": "_external.std.io.Error",
	// std primitive numeric (rarely impl'd against, but possible for newtypes)
	// (skipped — primitives can't be impl'd in foreign crates)
	// serde
	"Value":  "_external.serde_json.Value",
	"Map":    "_external.serde_json.Map",
	"Number": "_external.serde_json.Number",
	// chrono
	"DateTime":      "_external.chrono.DateTime",
	"NaiveDateTime": "_external.chrono.NaiveDateTime",
	"NaiveDate":     "_external.chrono.NaiveDate",
	"NaiveTime":     "_external.chrono.NaiveTime",
	"Utc":           "_external.chrono.Utc",
	"Local":         "_external.chrono.Local",
	"Timelike":      "_external.chrono.Timelike",
	// uuid
	"Uuid": "_external.uuid.Uuid",
	// bytes
	"Bytes":    "_external.bytes.Bytes",
	"BytesMut": "_external.bytes.BytesMut",
	// tokio runtime
	"Runtime":    "_external.tokio.runtime.Runtime",
	"Handle":     "_external.tokio.runtime.Handle",
	"JoinHandle": "_external.tokio.task.JoinHandle",
	// regex
	"Regex": "_external.regex.Regex",
	// thiserror
	// (no explicit struct names; uses derives)
	// anyhow
	"Error":   "_external.anyhow.Error", // NOTE: collides with std::error::Error?
	"Context": "_external.anyhow.Context",
	// url
	"Url":        "_external.url.Url",
	"ParseError": "_external.url.ParseError",
	// http
	"StatusCode":  "_external.http.StatusCode",
	"Method":      "_external.http.Method",
	"HeaderMap":   "_external.http.HeaderMap",
	"HeaderName":  "_external.http.HeaderName",
	"HeaderValue": "_external.http.HeaderValue",
	"Request":     "_external.http.Request",
	"Response":    "_external.http.Response",
}

// lookupSyntheticStruct returns the synthetic QN for a curated external
// struct/type name, or empty string + false if the name isn't in the
// registry. Mirror of lookupSyntheticTrait (synthetic_traits.go) for the
// struct side of `impl Trait for Struct`.
//
// Called by resolveAsClassWithReason as part of the 11th resolution
// strategy: if synthetic_traits doesn't match, try synthetic_structs.
// A name MAY be in BOTH registries (e.g. theoretical `Error` in both std
// and anyhow); the trait registry is tried first because trait names are
// more commonly external in idiomatic Rust impls.
func lookupSyntheticStruct(name string) (string, bool) {
	qn, ok := syntheticStructs[name]
	return qn, ok
}
