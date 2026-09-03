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
  cfg = config.services.appliedd;
in
{
  options.services.appliedd = {
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
        ${pkgs.submsg}/bin/submsg ${"builtins.concatStringsSep"} " " cfg.baf.sub_topics \
        | ${pkgs.appliedd}/bin/appliedd | ${pkgs.pubmsg}/bin/pubmsg ${"cfg.baf.pub_topic"}
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
  options.services.canstatd = {
    enable = mkEnableOption "canstatd";
  };

  config = mkIf cfg.enable {
    systemd.services.canstatd = {
      script = ''
        ${pkgs.canstatd}/bin/canstatd | ${pkgs.pubmsg}/bin/pubmsg canstatd
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
    services.mock-proteuscore.enable = true;
    services.tocarod.enable = true;
    services.trackerd.additional_sub_topics = [ "simd" "mock-data" ];
  };
}`

	info := parseNixServiceFile(src)

	// No options.services.X declaration — no service node from this file
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
		"topic ${pkgs.bin}":            "topic ",
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

// TestParseNixServiceFile_ConditionalSubTopics covers the apid.nix pattern
// of `default = [ ... ] ++ (if X then [...] else [...]) ++ lib.optionals Y [...]`.
func TestParseNixServiceFile_ConditionalSubTopics(t *testing.T) {
	t.Parallel()

	src := `{ config, lib, pkgs, ... }:
{
  options.services.apid = {
    baf.sub_topics = mkOption {
      type = types.listOf types.str;
      default = [
        "always_a"
        "always_b"
      ]
      ++ (if cfg.use_fuser then [ "fuserd" ] else [ "trackerd" ])
      ++ (if cfg.lift_enabled then [ "lift" "lift-aux" ] else [ ])
      ++ lib.optionals cfg.extras [ "extra_x" ];
    };
  };
}`

	info := parseNixServiceFile(src)

	wantBase := []string{"always_a", "always_b"}
	if !reflect.DeepEqual(info.subTopics, wantBase) {
		t.Errorf("subTopics (base): want %v got %v", wantBase, info.subTopics)
	}

	wantCond := map[string]bool{
		"fuserd":   true,
		"trackerd": true,
		"lift":     true,
		"lift-aux": true,
		"extra_x":  true,
	}
	if len(info.conditionalSubTopics) != len(wantCond) {
		t.Errorf("conditional count: want %d got %d (%v)",
			len(wantCond), len(info.conditionalSubTopics), info.conditionalSubTopics)
	}
	for _, t2 := range info.conditionalSubTopics {
		if !wantCond[t2] {
			t.Errorf("unexpected conditional topic: %q", t2)
		}
	}
}

// TestParseNixServiceFile_RunsBinary covers extraction of `${pkgs.X}/bin/Y`
// references with the pubmsg/submsg helpers filtered out.
func TestParseNixServiceFile_RunsBinary(t *testing.T) {
	t.Parallel()

	src := `{ config, lib, pkgs, ... }:
{
  options.services.demo = {
    enable = mkEnableOption "demo";
  };

  config = mkIf cfg.enable {
    systemd.services.demo = {
      script = ''
        ${pkgs.demo}/bin/demo \
          | ${pkgs.helper-tool}/bin/helper-tool \
          | ${pkgs.pubmsg}/bin/pubmsg demo
      '';
    };
  };
}`

	info := parseNixServiceFile(src)

	wantBins := map[string]bool{"demo": true, "helper-tool": true}
	if len(info.runsBinaries) != len(wantBins) {
		t.Errorf("runsBinaries count: want %d got %d (%v)",
			len(wantBins), len(info.runsBinaries), info.runsBinaries)
	}
	for _, b := range info.runsBinaries {
		if !wantBins[b] {
			t.Errorf("unexpected runs_binary: %q", b)
		}
	}
	// Confirm pubmsg was filtered out (it's a framework helper, not the implementation).
	for _, b := range info.runsBinaries {
		if b == "pubmsg" || b == "submsg" {
			t.Errorf("framework helper not filtered: %q", b)
		}
	}
}

// TestParseNixServiceFile_PubTopicVariants covers `baf.pub_topic_<suffix>` named
// variants — anavd has both `baf.pub_topic` and `baf.pub_topic_fast`.
func TestParseNixServiceFile_PubTopicVariants(t *testing.T) {
	t.Parallel()

	src := `{
  options.services.anavd = {
    baf.pub_topic = mkOption {
      type = types.str;
      default = "anavd";
    };
    baf.pub_topic_fast = mkOption {
      type = types.str;
      default = "anavd-fast";
    };
  };
}`

	info := parseNixServiceFile(src)

	if info.pubTopic != "anavd" {
		t.Errorf("primary pubTopic: want anavd got %q", info.pubTopic)
	}
	if len(info.pubTopicVariants) != 1 || info.pubTopicVariants[0] != "anavd-fast" {
		t.Errorf("pubTopicVariants: want [anavd-fast] got %v", info.pubTopicVariants)
	}
}

// TestParseNixServiceFile_SingularSubTopic covers adsbd's
// `baf.ahrs_sub_topic = "sbfd"` (singular scalar) pattern.
func TestParseNixServiceFile_SingularSubTopic(t *testing.T) {
	t.Parallel()

	src := `{
  options.services.adsbd = {
    baf.pub_topic = mkOption {
      type = types.str;
      default = "adsbd";
    };
    baf.ahrs_sub_topic = mkOption {
      type = types.str;
      default = "sbfd";
    };
  };
}`

	info := parseNixServiceFile(src)

	if info.pubTopic != "adsbd" {
		t.Errorf("pubTopic: want adsbd got %q", info.pubTopic)
	}
	if len(info.subTopics) != 1 || info.subTopics[0] != "sbfd" {
		t.Errorf("subTopics: want [sbfd] got %v", info.subTopics)
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

// TestParseNixServiceFile_CustomPrefixes covers organizations that nest their
// options under a namespace (options.acme.services.<name>) and their packages
// under a sub-set (${pkgs.acme.<pkg>}).
func TestParseNixServiceFile_CustomPrefixes(t *testing.T) {
	t.Parallel()

	src := `{ config, lib, pkgs, ... }:
{
  options.acme.services.alphad = {
    baf.pub_topic = mkOption { default = "alphad"; };
    baf.sub_topics = mkOption { default = [ "broker-a" ]; };
  };
  config = {
    systemd.services.alphad.script = ''
      ${pkgs.acme.submsg}/bin/submsg broker-a | ${pkgs.acme.alphad}/bin/alphad | ${pkgs.acme.pubmsg}/bin/pubmsg alphad
    '';
    acme.services.betad.additional_sub_topics = [ "extra" ];
  };
}`
	np := newNixPatterns("acme.services", "pkgs.acme")
	info := parseNixServiceFileWith(np, src)
	if info.serviceName != "alphad" {
		t.Fatalf("serviceName = %q, want alphad", info.serviceName)
	}
	if got := info.additionalSubsByService["betad"]; len(got) != 1 || got[0] != "extra" {
		t.Fatalf("additionalSubsByService[betad] = %v, want [extra]", got)
	}
	if len(info.runsBinaries) != 1 || info.runsBinaries[0] != "alphad" {
		t.Fatalf("runsBinaries = %v, want [alphad] (pubmsg/submsg filtered)", info.runsBinaries)
	}

	// The default patterns must NOT see a namespaced declaration.
	def := parseNixServiceFileWith(newNixPatterns("", ""), src)
	if def.serviceName != "" {
		t.Fatalf("default patterns matched namespaced service %q", def.serviceName)
	}
}

func TestSanitizeNixPrefix(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                "services",
		"  ":              "services",
		"services":        "services",
		".acme.services.": "acme.services",
		"bad prefix":      "services",
		"a;b":             "services",
	}
	for in, want := range cases {
		if got := sanitizeNixPrefix(in, "services"); got != want {
			t.Errorf("sanitizeNixPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
