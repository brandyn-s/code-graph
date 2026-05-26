package safegit

import (
	"context"
	"strings"
	"testing"
)

func TestCommandSetsEnvAndOverrides(t *testing.T) {
	cmd := Command(context.Background(), "status")

	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"core.fsmonitor=",
		"core.hooksPath=/dev/null",
		"core.sshCommand=echo",
		"core.pager=cat",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q: got %s", want, args)
		}
	}

	envMap := make(map[string]string)
	for _, e := range cmd.Env {
		k, v, _ := strings.Cut(e, "=")
		envMap[k] = v
	}
	if envMap["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Error("GIT_CONFIG_NOSYSTEM not set to 1")
	}
	if _, ok := envMap["PATH"]; !ok {
		t.Error("PATH not preserved in env")
	}
}

func TestCommandPreservesUserArgs(t *testing.T) {
	cmd := Command(context.Background(), "-C", "/tmp/repo", "rev-parse", "HEAD")
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "-C /tmp/repo rev-parse HEAD") {
		t.Errorf("user args not preserved: got %s", args)
	}
}

func TestSafeOverridesBeforeUserArgs(t *testing.T) {
	cmd := Command(context.Background(), "status")
	statusIdx := -1
	fsmonitorIdx := -1
	for i, a := range cmd.Args {
		if a == "core.fsmonitor=" {
			fsmonitorIdx = i
		}
		if a == "status" {
			statusIdx = i
		}
	}
	if fsmonitorIdx == -1 || statusIdx == -1 {
		t.Fatal("expected both core.fsmonitor= and status in args")
	}
	if fsmonitorIdx > statusIdx {
		t.Error("safe overrides must come before user args")
	}
}
