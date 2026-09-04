package cbm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// fuzzLanguages is the stable ordering the fuzzer maps its language byte onto.
// Only languages compiled into this build are included so a default build
// never spends fuzz time on the CUDA stub.
func fuzzLanguages() []lang.Language {
	var out []lang.Language
	for _, l := range lang.AllLanguages() {
		if lang.BuildIncludes(l) {
			out = append(out, l)
		}
	}
	return out
}

// seedFromFixtures adds every parseable file under the synthetic accuracy
// fixtures and grammar canaries as a (language, source) seed pair, so the
// fuzzer starts from real programs in every supported grammar instead of
// random bytes.
func seedFromFixtures(f *testing.F, languages []lang.Language) {
	index := map[lang.Language]int{}
	for i, l := range languages {
		index[l] = i
	}
	roots := []string{
		filepath.Join("..", "..", "bench", "accuracy", "synthetic"),
		filepath.Join("..", "..", "bench", "research", "grammar_canaries"),
	}
	added := 0
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || info.Size() > 64*1024 {
				return nil //nolint:nilerr // fuzz seeding skips unreadable entries by design
			}
			l, ok := lang.LanguageForFilename(filepath.Base(path))
			if !ok {
				l, ok = lang.LanguageForExtension(filepath.Ext(path))
			}
			if !ok {
				return nil
			}
			i, ok := index[l]
			if !ok {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil //nolint:nilerr // fuzz seeding skips unreadable entries by design
			}
			f.Add(uint8(i), data)
			added++
			return nil
		})
	}
	if added == 0 {
		f.Fatalf("no fixture seeds found under %v", roots)
	}
}

// FuzzExtractFile asserts the tree-sitter parse plus the hand-written C
// extractors never crash, hang, or corrupt memory on arbitrary input in any
// compiled grammar. Extraction errors are acceptable; panics and sanitizer
// reports are not. The seed corpus doubles as a regression suite in ordinary
// `go test` runs. Nightly fuzzing runs this under AddressSanitizer:
//
//	CGO_CFLAGS="-fsanitize=address -fno-omit-frame-pointer" CGO_LDFLAGS="-fsanitize=address" \
//	  go test -run=^$ -fuzz=FuzzExtractFile -fuzztime=10m ./internal/cbm/
func FuzzExtractFile(f *testing.F) {
	languages := fuzzLanguages()
	if len(languages) == 0 {
		f.Skip("no languages compiled into this build")
	}
	seedFromFixtures(f, languages)
	// Degenerate inputs every grammar must survive.
	for i := range languages {
		f.Add(uint8(i), []byte{})
		f.Add(uint8(i), []byte("\x00"))
		f.Add(uint8(i), []byte("{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{"))
		f.Add(uint8(i), []byte("\xff\xfe\xfd invalid utf8 \xc3\x28"))
	}
	f.Fuzz(func(t *testing.T, langByte uint8, source []byte) {
		l := languages[int(langByte)%len(languages)]
		// Property under test: returns (result or error) without panicking.
		_, _ = ExtractFile(source, l, "fuzz", "fuzz."+string(l))
	})
}
