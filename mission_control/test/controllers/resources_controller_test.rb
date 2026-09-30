require "test_helper"
require_relative "../support/fake_docker"
require_relative "../support/project_helpers"

# The flight board's resources, which an open board asks for every 30 seconds
# (docs/plans/app-stats.md): read now, as Turbo Stream updates of each
# project's row and card.
class ResourcesControllerTest < ActionDispatch::IntegrationTest
  include FakeDockerHelper
  include ProjectHelpers

  setup do
    make_project("equip")
    make_project("cart")
    Rails.cache.delete(AppStats::CACHE_KEY)
  end

  def docker
    stats = [ { "ID" => "w1", "Name" => "equip-web-#{"a" * 40}", "CPUPerc" => "0.30%", "MemUsage" => "7MiB / 15.6GiB" } ].map(&:to_json).join("\n")
    FakeDocker.new do |args|
      case args.first
      when "stats" then DockerCommand::Result.new(success: true, output: stats)
      when "inspect" then DockerCommand::Result.new(success: true, output: "w1 0 0")
      when "system" then DockerCommand::Result.new(success: true, output: "[]")
      when "info" then DockerCommand::Result.new(success: true, output: "8 16000000000")
      end
    end
  end

  # The stream updating every element a project's resources are in.
  def stream(name) = Nokogiri::HTML5.fragment(response.body).css("turbo-stream").find { it["targets"] == "[data-resources='#{name}']" }

  def template(stream) = Nokogiri::HTML5.fragment(stream.at_css("template").inner_html)

  test "each project's gauges, read now" do
    sign_in_as users(:one)
    use_fake_docker(docker) do |fake|
      get resources_path, headers: { "Accept" => "text/vnd.turbo-stream.html" }
      assert_equal 1, fake.calls.count { |c| c.args.first == "stats" }
    end
    assert_response :success
    assert_equal "text/vnd.turbo-stream.html", response.media_type
    equip, cart = stream("equip"), stream("cart")
    assert_equal "update", equip["action"]
    assert_equal "0.003 cores", template(equip).at_css(".usage--cpu .usage__amount").text
    assert_match "—", template(cart).text, "nothing of it running"

    use_fake_docker(docker) do |fake|
      get resources_path, headers: { "Accept" => "text/vnd.turbo-stream.html" }
      assert_equal 0, fake.calls.count { |c| c.args.first == "stats" }, "a second board, within 25 seconds: the same reading"
    end
  end

  test "signed out, nothing" do
    use_fake_docker(docker) do |fake|
      get resources_path
      assert_equal 0, fake.calls.size
    end
    assert_response :redirect
  end
end
