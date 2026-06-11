package lang

// Language represents a supported programming language.
type Language string

const (
	Python     Language = "python"
	JavaScript Language = "javascript"
	TypeScript Language = "typescript"
	Go         Language = "go"
	Rust       Language = "rust"
	Java       Language = "java"
	CPP        Language = "cpp"
	TSX        Language = "tsx"
	// Programming languages (Tier 1)
	C          Language = "c"
	Bash       Language = "bash"
	PowerShell Language = "powershell"
	Nix        Language = "nix"
	CUDA       Language = "cuda"

	// Helper languages (Tier 2)
	HTML       Language = "html"
	CSS        Language = "css"
	SCSS       Language = "scss"
	YAML       Language = "yaml"
	TOML       Language = "toml"
	HCL        Language = "hcl"
	SQL        Language = "sql"
	Dockerfile Language = "dockerfile"
	JSON       Language = "json"
	XML        Language = "xml"
	Markdown   Language = "markdown"
	Makefile   Language = "makefile"
	CMake      Language = "cmake"
	Protobuf   Language = "protobuf"
)

// AllLanguages returns all supported languages.
func AllLanguages() []Language {
	return []Language{
		Python, JavaScript, TypeScript, TSX, Go, Rust, Java, CPP,
		C, Bash, PowerShell, Nix, CUDA,
		HTML, CSS, SCSS, YAML, TOML, HCL, SQL, Dockerfile,
		JSON, XML, Markdown, Makefile, CMake, Protobuf,
	}
}

// LanguageSpec defines the tree-sitter node types for a language.
type LanguageSpec struct {
	Language          Language
	FileExtensions    []string
	FunctionNodeTypes []string
	ClassNodeTypes    []string
	FieldNodeTypes    []string // tree-sitter node kinds for struct/class fields
	ModuleNodeTypes   []string
	CallNodeTypes     []string
	ImportNodeTypes   []string
	ImportFromTypes   []string
	PackageIndicators []string

	// BranchingNodeTypes lists AST node kinds counted for complexity metric.
	BranchingNodeTypes []string
	// VariableNodeTypes lists module-level variable declaration node kinds.
	VariableNodeTypes []string
	// AssignmentNodeTypes lists assignment expression/statement node kinds.
	AssignmentNodeTypes []string
	// ThrowNodeTypes lists throw/raise statement node kinds.
	ThrowNodeTypes []string
	// ThrowsClauseField is the field name for declared throws (e.g. Java "throws").
	ThrowsClauseField string
	// DecoratorNodeTypes lists decorator/annotation node kinds.
	DecoratorNodeTypes []string
	// EnvAccessFunctions lists function names used to read env vars (e.g. "os.Getenv").
	EnvAccessFunctions []string
	// EnvAccessMemberPatterns lists member access patterns for env vars (e.g. "process.env").
	EnvAccessMemberPatterns []string
}

// registry maps file extensions to language specs.
var registry = map[string]*LanguageSpec{}

// Register adds a LanguageSpec to the global registry.
func Register(spec *LanguageSpec) {
	for _, ext := range spec.FileExtensions {
		registry[ext] = spec
	}
}

// ForExtension returns the LanguageSpec for a file extension (e.g. ".go").
func ForExtension(ext string) *LanguageSpec {
	return registry[ext]
}

// ForLanguage returns the LanguageSpec for a language.
func ForLanguage(lang Language) *LanguageSpec {
	for _, spec := range registry {
		if spec.Language == lang {
			return spec
		}
	}
	return nil
}

// LanguageForExtension returns the Language for a file extension.
func LanguageForExtension(ext string) (Language, bool) {
	spec := registry[ext]
	if spec == nil {
		return "", false
	}
	return spec.Language, true
}

// filenameToLanguage maps exact filenames to languages (for extensionless files).
var filenameToLanguage = map[string]Language{
	"Makefile":       Makefile,
	"GNUmakefile":    Makefile,
	"makefile":       Makefile,
	"CMakeLists.txt": CMake,
	"Dockerfile":     Dockerfile,
}

// LanguageForFilename returns the Language for an exact filename match.
func LanguageForFilename(name string) (Language, bool) {
	l, ok := filenameToLanguage[name]
	return l, ok
}
