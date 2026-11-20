# Homebrew formula for argentgo.
#
# NOTE: the native desktop GUI (argent-native) is Windows-only and is NOT
# installed by this formula. It builds the CLI/TUI binary (argent) from source
# because published binary releases are Windows-only.
class Argentgo < Formula
  desc "Personal coding-agent harness: streaming replies, tools and JSONL sessions"
  homepage "https://github.com/2118505078xyh1/argentgo"
  url "https://github.com/2118505078xyh1/argentgo/archive/refs/tags/v{{VERSION}}.tar.gz"
  version "{{VERSION}}"
  license "Apache-2.0"
  head "https://github.com/2118505078xyh1/argentgo.git", branch: "main"

  depends_on "go" => :build

  def install
    # Send go install's output straight into the formula's bin directory.
    ENV["GOBIN"] = bin
    system "go", "install", "github.com/2118505078xyh1/argentgo/cmd/argent@{{VERSION}}"
  end

  test do
    assert_predicate bin / "argent", :exist?
  end
end
