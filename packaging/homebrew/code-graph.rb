# Homebrew formula template for code-graph.
#
# scripts/update-homebrew-formula.sh <tag> fills VERSION and every SHA256 from
# the release's checksums.txt and writes the result to a tap checkout
# (brandyn-s/homebrew-tap, Formula/code-graph.rb). Do not edit the filled
# copy by hand; edit this template and re-run the script.
class CodeGraph < Formula
  desc "Persistent code knowledge graph MCP server with source-backed evidence"
  homepage "https://github.com/brandyn-s/code-graph"
  version "@VERSION@"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/brandyn-s/code-graph/releases/download/v@VERSION@/code-graph-darwin-arm64.tar.gz"
      sha256 "@SHA256_DARWIN_ARM64@"
    end
    on_intel do
      url "https://github.com/brandyn-s/code-graph/releases/download/v@VERSION@/code-graph-darwin-amd64.tar.gz"
      sha256 "@SHA256_DARWIN_AMD64@"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/brandyn-s/code-graph/releases/download/v@VERSION@/code-graph-linux-arm64.tar.gz"
      sha256 "@SHA256_LINUX_ARM64@"
    end
    on_intel do
      url "https://github.com/brandyn-s/code-graph/releases/download/v@VERSION@/code-graph-linux-amd64.tar.gz"
      sha256 "@SHA256_LINUX_AMD64@"
    end
  end

  def install
    bin.install "code-graph"
  end

  def caveats
    <<~EOS
      Register the server with your MCP client, for example:
        claude mcp add code-graph --scope user -- #{opt_bin}/code-graph
      Run `code-graph doctor` to see the resolved configuration.
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/code-graph --version")
    system bin/"code-graph", "doctor", "--json"
  end
end
