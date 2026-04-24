package pipeline

import (
	"reflect"
	"sort"
	"testing"
)

// TestParseNixServiceFile_AppliedPattern covers the canonical PSM module
// shape: service declaration with baf.pub_topic + baf.sub_topics mkOption
// defaults. Modeled after nix/modules/appliedd.nix.
func TestParseNixServiceFile_AppliedPattern(t *testing.T) {
	t.Parallel()

	src := `{ config, lib, pkgs, ... }:
with lib;
let
  cfg = config.redacted.services.appliedd;
in
{
  options.redacted.services.appliedd = {
    enable = mkEnableOption "appliedd";
    baf.pub_topic = mkOption {
      type = types.str;
      description = "baf publisher topic";
      default = "sbfd";
    };
    baf.sub_topics = mkOption {
      type = types.listOf types.str;
      description = "baf subscriber topics";
      default = [ "baf-test" "controlsd" ];
    };
  };

  config = mkIf cfg.enable {
    systemd.services.appliedd = {
      script = ''
        ${pkgs.redacted.submsg}/bin/submsg ${"builtins.concatStringsSep"} " " cfg.baf.sub_topics \
        | ${pkgs.redacted.appliedd}/bin/appliedd | ${pkgs.redacted.pubmsg}/bin/pubmsg ${"cfg.baf.pub_topic"}
      '';
    };
  };
}`

	info := parseNixServiceFile(src)

	if info.serviceName != "appliedd" {
		t.Errorf("serviceName: want %q got %q", "appliedd", info.serviceName)
	}
	if info.pubTopic != "sbfd" {
		t.Errorf("pubTopic: want %q got %q", "sbfd", info.pubTopic)
	}
	wantSubs := []string{"baf-test", "controlsd"}
	if !reflect.DeepEqual(info.subTopics, wantSubs) {
		t.Errorf("subTopics: want %v got %v", wantSubs, info.subTopics)
	}
	// impPubTopics should be empty because the pubmsg arg is ${...}-templated
	if len(info.impPubTopics) != 0 {
		t.Errorf("impPubTopics: want empty got %v", info.impPubTopics)
	}
}

// TestParseNixServiceFile_HardcodedTopic covers modules like canstatd.nix
// that hardcode the topic as a literal in the script, with no mkOption.
func TestParseNixServiceFile_HardcodedTopic(t *testing.T) {
	t.Parallel()

	src := `{ config, lib, pkgs, ... }:
{
  options.redacted.services.canstatd = {
    enable = mkEnableOption "canstatd";
  };

  config = mkIf cfg.enable {
    systemd.services.canstatd = {
      script = ''
        ${pkgs.redacted.canstatd}/bin/canstatd | ${pkgs.redacted.pubmsg}/bin/pubmsg canstatd
      '';
    };
  };
}`

	info := parseNixServiceFile(src)

	if info.serviceName != "canstatd" {
		t.Errorf("serviceName: want %q got %q", "canstatd", info.serviceName)
	}
	if info.pubTopic != "" {
		t.Errorf("pubTopic: want empty (no mkOption) got %q", info.pubTopic)
	}
	if len(info.impPubTopics) != 1 || info.impPubTopics[0] != "canstatd" {
		t.Errorf("impPubTopics: want [canstatd] got %v", info.impPubTopics)
	}
}

// TestParseNixServiceFile_AdditionalSubTopics covers the
// nazgul-radar-services.nix pattern of adding subscriptions to an existing
// service declared elsewhere.
func TestParseNixServiceFile_AdditionalSubTopics(t *testing.T) {
	t.Parallel()

	src := `{ pkgs, config, lib, ... }:
{
  config = {
    redacted.services.mock-proteuscore.enable = true;
    redacted.services.tocarod.enable = true;
    redacted.services.trackerd.additional_sub_topics = [ "simd" "mock-data" ];
  };
}`

	info := parseNixServiceFile(src)

	// No options.redacted.services.X declaration — no service node from this file
	if info.serviceName != "" {
		t.Errorf("serviceName: want empty got %q", info.serviceName)
	}
	// But additional_sub_topics SHOULD be captured
	if len(info.additionalSubsByService) != 1 {
		t.Fatalf("additionalSubsByService count: want 1 got %d: %v",
			len(info.additionalSubsByService), info.additionalSubsByService)
	}
	topics, ok := info.additionalSubsByService["trackerd"]
	if !ok {
		t.Fatalf("additionalSubsByService missing trackerd key")
	}
	sort.Strings(topics)
	want := []string{"mock-data", "simd"}
	if !reflect.DeepEqual(topics, want) {
		t.Errorf("additional topics for trackerd: want %v got %v", want, topics)
	}
}

// TestExtractNixStringList covers the raw list parser.
func TestExtractNixStringList(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want []string
	}{
		{"single", `"foo"`, []string{"foo"}},
		{"multiple", `"foo" "bar" "baz"`, []string{"foo", "bar", "baz"}},
		{"with_whitespace", `  "foo"
        "bar"
      `, []string{"foo", "bar"}},
		{"empty", ``, []string{}},
		{"hyphen_underscore", `"baf-test" "my_topic"`, []string{"baf-test", "my_topic"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractNixStringList(tc.body)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("want %v got %v", tc.want, got)
			}
		})
	}
}

// TestStripNixInterpolations ensures ${...} chunks (including nested) are removed.
func TestStripNixInterpolations(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"hello ${world}":               "hello ",
		"${a} ${b} c":                  "  c",
		"topic ${pkgs.redacted.bin}":    "topic ",
		"${outer ${inner}} after":      " after",
		"no interpolation":             "no interpolation",
		"${builtins.toString cfg.x} z": " z",
	}
	for input, want := range cases {
		if got := stripNixInterpolations(input); got != want {
			t.Errorf("stripNixInterpolations(%q):\n  want %q\n  got  %q", input, want, got)
		}
	}
}

// TestExtractSubmsgTopics covers imperative submsg argument parsing.
func TestExtractSubmsgTopics(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"literal_bare", `trackerd tocarod simd`, []string{"trackerd", "tocarod", "simd"}},
		{"literal_quoted", `"topic-a" "topic-b"`, []string{"topic-a", "topic-b"}},
		{"all_templated", `${builtins.concatStringsSep " " cfg.baf.sub_topics}`, []string(nil)},
		{"mixed_keyword_filtered", `cfg baf trackerd`, []string{"trackerd"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractSubmsgTopics(tc.input)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("want %v got %v", tc.want, got)
			}
		})
	}
}

// TestUniqueStrings sanity check.
func TestUniqueStrings(t *testing.T) {
	t.Parallel()

	got := uniqueStrings([]string{"a", "b", "a", "c", "b"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %v got %v", want, got)
	}
}
