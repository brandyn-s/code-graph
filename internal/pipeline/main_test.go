package pipeline

import (
	"os"
	"testing"

	"github.com/brandyn-s/code-graph/internal/cbm/isolate"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Worker mode: the extraction-isolation tests relaunch this test binary as
	// the supervised worker (production uses `code-graph cbm-extract-worker`).
	if os.Getenv("GO_ISOLATE_WORKER") == "1" {
		if err := isolate.RunWorker(os.Stdin, os.Stdout); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	goleak.VerifyTestMain(m,
		// HTTP transport dial goroutines from version checker
		goleak.IgnoreTopFunction("net/http.(*Transport).dialConnFor"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)
}
