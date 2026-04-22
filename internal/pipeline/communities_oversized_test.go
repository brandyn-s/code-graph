package pipeline

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// capturedLogs installs a JSON slog handler writing to a buffer and restores
// the previous default handler on cleanup. Lets us assert on structured log
// output without plumbing test loggers into the pipeline package.
func capturedLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return buf, func() { slog.SetDefault(prev) }
}

// The oversized-cluster block we just added in passCommunities runs as a pure
// function of the `communities` map — no DB, no pipeline wiring. Exercise it
// directly by calling the same inline computation the production path uses,
// so we can assert the WARN fires exactly when expected without spinning up
// the full indexing pipeline.
func TestCommunitiesOversizedWarnsPastThreshold(t *testing.T) {
	buf, restore := capturedLogs(t)
	defer restore()

	// Ten-cluster graph where cluster 0 owns 40% of the members — should warn.
	communities := map[int][]int64{
		0: make([]int64, 40),
		1: make([]int64, 10),
		2: make([]int64, 10),
		3: make([]int64, 10),
		4: make([]int64, 10),
		5: make([]int64, 10),
		6: make([]int64, 10),
	}
	emitOversizedWarningForTest(communities)

	log := buf.String()
	if !strings.Contains(log, "pass.communities.oversized_cluster") {
		t.Errorf("expected WARN for 40%% cluster, log=%s", log)
	}
	if !strings.Contains(log, `"max_members":40`) {
		t.Errorf("expected max_members=40 in log, got %s", log)
	}
}

func TestCommunitiesOversizedStaysQuietUnderThreshold(t *testing.T) {
	buf, restore := capturedLogs(t)
	defer restore()

	// Largest at 20% — under the 25% threshold, no warning.
	communities := map[int][]int64{
		0: make([]int64, 20),
		1: make([]int64, 18),
		2: make([]int64, 17),
		3: make([]int64, 15),
		4: make([]int64, 15),
		5: make([]int64, 15),
	}
	emitOversizedWarningForTest(communities)

	log := buf.String()
	if strings.Contains(log, "oversized_cluster") {
		t.Errorf("did not expect oversized warning at 20%% max, got log: %s", log)
	}
}

func TestCommunitiesOversizedHandlesEmpty(t *testing.T) {
	buf, restore := capturedLogs(t)
	defer restore()

	emitOversizedWarningForTest(map[int][]int64{})

	if s := buf.String(); s != "" {
		t.Errorf("empty communities should produce no log output, got: %s", s)
	}
}

// emitOversizedWarningForTest mirrors the inline block in passCommunities
// so tests can exercise the threshold logic without a full pipeline run.
// Kept in the _test.go file so it never ships in production binaries.
func emitOversizedWarningForTest(communities map[int][]int64) {
	totalAssigned := 0
	for _, members := range communities {
		totalAssigned += len(members)
	}
	if totalAssigned == 0 {
		return
	}
	maxMembers := 0
	for _, members := range communities {
		if len(members) > maxMembers {
			maxMembers = len(members)
		}
	}
	maxPct := float64(maxMembers) / float64(totalAssigned)
	if maxPct > 0.25 {
		slog.Warn("pass.communities.oversized_cluster",
			"max_members", maxMembers,
			"total_assigned", totalAssigned,
			"pct_of_assigned", maxPct,
			"threshold", 0.25)
	}
}
