package pipeline

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// TestFindZenohSites covers the core regex patterns against synthetic
// Rust source that mirrors the libio::zenoh API surface. Real PSM code
// uses identical patterns — if this passes, extraction works on PSM.
func TestFindZenohSites(t *testing.T) {
	t.Parallel()

	src := `
use libio::zenoh::r#async::{AsyncPublisher, AsyncSubscriberFifo};
use libio::zenoh::sync::SyncPublisherThrottled;

async fn main() -> anyhow::Result<()> {
    let session = AsyncSession::new("canstatd")?;

    // Publisher - relative topic (local scope)
    let pub_a = AsyncPublisher::new(&session, "can/status").await?;

    // Publisher throttled - external scope
    let pub_b = SyncPublisherThrottled::new_external(&session, "shared/alerts", std::time::Duration::from_secs(1), "sg1")?;

    // Publisher unrestricted - absolute scope
    let pub_c = AsyncPublisher::new_unrestricted(&session, "asset/sg2/raw").await?;

    // Subscriber FIFO - local scope
    let sub_a = AsyncSubscriberFifo::new(&session, "controls/manual", Some(10)).await?;

    // Session-owned subscriber
    session.create_subscriber("navigation/gps")?;
    session.create_subscriber_external("shared/fleet", "sg1")?;
    session.create_subscriber_unrestricted("asset/sg3/diag")?;

    // Querier (request client)
    let q = AsyncQuerier::new(&session, "query/health", std::time::Duration::from_secs(5)).await?;

    // Queryable (request server)
    let qa = AsyncQueryableFifo::new(&session, "service/ping", Some(100)).await?;

    Ok(())
}
`
	sites := findZenohSites(src)

	want := map[string]string{
		"can/status":      "Publisher",
		"shared/alerts":   "Publisher",
		"asset/sg2/raw":   "Publisher",
		"controls/manual": "Subscriber",
		"navigation/gps":  "Subscriber",
		"shared/fleet":    "Subscriber",
		"asset/sg3/diag":  "Subscriber",
		"query/health":    "Querier",
		"service/ping":    "Queryable",
	}

	got := make(map[string]string, len(sites))
	for _, s := range sites {
		got[s.topic] = s.role
	}

	for topic, role := range want {
		if got[topic] != role {
			t.Errorf("topic %q: want role=%q, got %q", topic, role, got[topic])
		}
	}

	if len(sites) != len(want) {
		t.Errorf("detected %d sites, want %d; sites=%v", len(sites), len(want), sites)
	}
}

// TestZenohSiteProperties checks that method, scope, throttled, and
// bufferType are correctly inferred from the regex matches.
func TestZenohSiteProperties(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		src         string
		wantRole    string
		wantMethod  string
		wantScope   string
		wantThrott  bool
		wantBufType string
	}{
		{
			name:        "publisher local",
			src:         `AsyncPublisher::new(&s, "can/status").await`,
			wantRole:    "Publisher",
			wantMethod:  "new",
			wantScope:   "local",
			wantThrott:  false,
			wantBufType: "",
		},
		{
			name:        "publisher throttled external",
			src:         `SyncPublisherThrottled::new_external(&s, "shared/x", d, "sg1")`,
			wantRole:    "Publisher",
			wantMethod:  "new_external",
			wantScope:   "external",
			wantThrott:  true,
			wantBufType: "",
		},
		{
			name:        "publisher unrestricted absolute",
			src:         `AsyncPublisher::new_unrestricted(&s, "asset/sg1/raw")`,
			wantRole:    "Publisher",
			wantMethod:  "new_unrestricted",
			wantScope:   "absolute",
			wantThrott:  false,
			wantBufType: "",
		},
		{
			name:        "subscriber ring local",
			src:         `AsyncSubscriberRing::new(&s, "nav/gps", None).await`,
			wantRole:    "Subscriber",
			wantMethod:  "new",
			wantScope:   "local",
			wantThrott:  false,
			wantBufType: "ring",
		},
		{
			name:        "subscriber fifo external",
			src:         `SyncSubscriberFifo::new_external(&s, "shared/z", None, "sg2")`,
			wantRole:    "Subscriber",
			wantMethod:  "new_external",
			wantScope:   "external",
			wantThrott:  false,
			wantBufType: "fifo",
		},
		{
			name:        "session create_subscriber",
			src:         `session.create_subscriber("nav/compass")`,
			wantRole:    "Subscriber",
			wantMethod:  "create_subscriber",
			wantScope:   "local",
			wantThrott:  false,
			wantBufType: "",
		},
		{
			name:        "session create_subscriber_external",
			src:         `session.create_subscriber_external("shared/team", "sg3")`,
			wantRole:    "Subscriber",
			wantMethod:  "create_subscriber_external",
			wantScope:   "external",
			wantThrott:  false,
			wantBufType: "",
		},
		{
			name:        "querier",
			src:         `AsyncQuerier::new(&s, "query/x", d).await`,
			wantRole:    "Querier",
			wantMethod:  "new",
			wantScope:   "local",
			wantThrott:  false,
			wantBufType: "",
		},
		{
			name:        "queryable ring",
			src:         `SyncQueryableRing::new(&s, "serve/y", Some(50))`,
			wantRole:    "Queryable",
			wantMethod:  "new",
			wantScope:   "local",
			wantThrott:  false,
			wantBufType: "ring",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sites := findZenohSites(tc.src)
			if len(sites) != 1 {
				t.Fatalf("want exactly 1 site, got %d: %v", len(sites), sites)
			}
			s := sites[0]
			if s.role != tc.wantRole {
				t.Errorf("role: want %q got %q", tc.wantRole, s.role)
			}
			if s.method != tc.wantMethod {
				t.Errorf("method: want %q got %q", tc.wantMethod, s.method)
			}
			if s.scope != tc.wantScope {
				t.Errorf("scope: want %q got %q", tc.wantScope, s.scope)
			}
			if s.throttled != tc.wantThrott {
				t.Errorf("throttled: want %v got %v", tc.wantThrott, s.throttled)
			}
			if s.bufferType != tc.wantBufType {
				t.Errorf("bufferType: want %q got %q", tc.wantBufType, s.bufferType)
			}
		})
	}
}

// TestZenohEdgeType ensures role → edge type mapping is exhaustive.
func TestZenohEdgeType(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"Publisher":  "PUBLISHES_TO",
		"Subscriber": "SUBSCRIBES_TO",
		"Querier":    "QUERIES",
		"Queryable":  "ANSWERS",
		"unknown":    "",
	}
	for role, want := range cases {
		if got := zenohEdgeType(role); got != want {
			t.Errorf("zenohEdgeType(%q): want %q got %q", role, want, got)
		}
	}
}

// TestSanitizeTopicForQN ensures topic expressions become valid QN segments.
func TestSanitizeTopicForQN(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"can/status":       "can.status",
		"shared/alerts/v2": "shared.alerts.v2",
		"asset/sg1/raw":    "asset.sg1.raw",
		"/leading/slash":   "leading.slash",
		"trailing/slash/":  "trailing.slash",
		"topic with space": "topic_with_space",
		"simple":           "simple",
	}
	for input, want := range cases {
		if got := sanitizeTopicForQN(input); got != want {
			t.Errorf("sanitizeTopicForQN(%q): want %q got %q", input, want, got)
		}
	}
}

// TestIsSharedTopic ensures shared-namespace detection matches
// libio::zenoh::common::is_shared_topic semantics.
func TestIsSharedTopic(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"shared/alerts":      true,
		"asset/sg1/shared/x": true,
		"controls/manual":    false,
		"asset/sg1/controls": false,
	}
	for topic, want := range cases {
		if got := isSharedTopic(topic); got != want {
			t.Errorf("isSharedTopic(%q): want %v got %v", topic, want, got)
		}
	}
}

// TestFindEnclosingFunction covers the line-range lookup.
func TestFindEnclosingFunction(t *testing.T) {
	t.Parallel()

	// Fake Function nodes - we only need the line ranges.
	// Simulates a file with main() at 1-50 containing a nested closure at 20-30.
	ranges := []funcRange{
		{start: 1, end: 50, node: &store.Node{Name: "main"}},
		{start: 20, end: 30, node: &store.Node{Name: "closure"}},
		{start: 60, end: 80, node: &store.Node{Name: "helper"}},
	}

	cases := []struct {
		line int
		want string
	}{
		{5, "main"},     // Only main covers line 5.
		{25, "closure"}, // Both main and closure cover 25; closure is narrower.
		{40, "main"},    // Only main covers 40.
		{70, "helper"},  // Only helper covers 70.
		{100, ""},       // No function covers 100.
	}
	for _, tc := range cases {
		got := findEnclosingFunction(ranges, tc.line)
		gotName := ""
		if got != nil {
			gotName = got.Name
		}
		if gotName != tc.want {
			t.Errorf("line %d: want %q got %q", tc.line, tc.want, gotName)
		}
	}
}
