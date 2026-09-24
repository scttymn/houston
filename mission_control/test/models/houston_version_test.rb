require "test_helper"

# Which Houston this is (docs/plans/releases.md, row 1): a release's tag, baked
# into the published image; a checkout's commit, when the installer built it
# from source; or dev.
class HoustonVersionTest < ActiveSupport::TestCase
  def with_env(values)
    saved = values.keys.index_with { |k| ENV[k] }
    values.each { |k, v| ENV[k] = v }
    yield
  ensure
    saved.each { |k, v| ENV[k] = v }
  end

  test "the version is the release, the source commit, or dev" do
    with_env("HOUSTON_VERSION" => "v0.1.0", "HOUSTON_SOURCE_SHA" => "4c4f80a1234") { assert_equal "v0.1.0", HoustonVersion.current }
    with_env("HOUSTON_VERSION" => nil, "HOUSTON_SOURCE_SHA" => "4c4f80a1234abcd") { assert_equal "source 4c4f80a", HoustonVersion.current }
    with_env("HOUSTON_VERSION" => "", "HOUSTON_SOURCE_SHA" => "") { assert_equal "dev", HoustonVersion.current }
    with_env("HOUSTON_VERSION" => "v0.1.0; rm -rf", "HOUSTON_SOURCE_SHA" => "not a sha") { assert_equal "dev", HoustonVersion.current, "only a tag or a hex commit" }
  end
end
