# Homebrew formula for argentgo.
#
# NOTE: the native desktop GUI (argent-native) is Windows-only and is NOT
# installed by this formula. It builds the CLI/TUI binary (argent) from source
# because published binary releases are Windows-only.
class Argentgo < Formula
  desc "Personal coding-agent harness: streaming replies, tools and JSONL sessions"
  homepage "https://example.com/argentgo"
  url "https://example.com/argentgo/archive/refs/tags/v{{VERSION}}.tar.gz"
  version "{{VERSION}}"
  license "Apache-2.0"
  head "https://example.com/argentgo.git", branch: "main"

  depends_on "go" => :build

  def install
    # Send go install's output straight into the formula's bin directory.
    ENV["GOBIN"] = bin
    # go install 产出的名字由导入路径末段决定，即 `argent`；这里改名为品牌名
    # `argentgo`，并用带 v 前缀的 tag 作为 @version 查询版本。
    system "go", "install", "example.com/argentgo/cmd/argent@v{{VERSION}}"
    File.rename(bin / "argent", bin / "argentgo")
  end

  test do
    assert_predicate bin / "argentgo", :exist?
  end
end
