package pipeline

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestStaticDispatchDebug_SlogEmitsOnResolve is the verify-effectiveness.md
// pre-flight: emit one expected event on a known-positive fixture and confirm
// it lands in the slog stream. Without this, a 23-minute production run could
// produce zero records if routing is broken (2026-05-08 A1 incident).
func TestStaticDispatchDebug_SlogEmitsOnResolve(t *testing.T) {
	// Save and restore the package-level gate.
	prev := staticDispatchDebugEnabled
	staticDispatchDebugEnabled = true
	defer func() { staticDispatchDebugEnabled = prev }()

	// Capture slog into a buffer.
	var buf bytes.Buffer
	prevDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prevDefault)

	// Known-positive registration: AssetRepo::new resolves intra-crate.
	r := NewFunctionRegistry()
	r.Register("AssetRepo", "proj.src.repo.AssetRepo", "Struct")
	r.Register("new", "proj.src.repo.AssetRepo.new", "Method")

	ctx := CallContext{
		CalleeName:     "AssetRepo::new",
		CallerQN:       "proj.src.main.controls",
		ModuleQN:       "proj.src.main",
		ImportBindings: map[string]string{"AssetRepo": "crate::repo::AssetRepo"},
	}
	result := r.ResolveCtx(ctx)
	if result.QualifiedName != "proj.src.repo.AssetRepo.new" {
		t.Fatalf("pre-condition failed: expected resolution, got %q", result.QualifiedName)
	}

	output := buf.String()
	if !strings.Contains(output, "static_dispatch.outcome") {
		t.Fatalf("expected 'static_dispatch.outcome' slog record, got: %q", output)
	}
	if !strings.Contains(output, `outcome=resolved`) {
		t.Fatalf("expected outcome=resolved in slog, got: %q", output)
	}
	if !strings.Contains(output, `type_name=AssetRepo`) {
		t.Fatalf("expected type_name=AssetRepo in slog, got: %q", output)
	}
}

// TestStaticDispatchDebug_SlogEmitsOnDrop confirms the slog also fires for
// the drop_type_not_registered outcome — the most informative case for the
// bucket-B FN classification.
func TestStaticDispatchDebug_SlogEmitsOnDrop(t *testing.T) {
	prev := staticDispatchDebugEnabled
	staticDispatchDebugEnabled = true
	defer func() { staticDispatchDebugEnabled = prev }()

	var buf bytes.Buffer
	prevDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prevDefault)

	// Vec is std-external — no internal class registered.
	r := NewFunctionRegistry()

	ctx := CallContext{
		CalleeName:     "Vec::new",
		CallerQN:       "proj.src.main.run",
		ModuleQN:       "proj.src.main",
		ImportBindings: map[string]string{},
	}
	result := r.ResolveCtx(ctx)
	if result.QualifiedName != "" {
		t.Fatalf("pre-condition failed: expected drop, got %q", result.QualifiedName)
	}

	output := buf.String()
	if !strings.Contains(output, `outcome=drop_type_not_registered`) {
		t.Fatalf("expected outcome=drop_type_not_registered in slog, got: %q", output)
	}
}
