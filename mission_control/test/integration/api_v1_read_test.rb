require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"

class ApiV1ReadTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeDockerHelper

  setup do
    @token, = ApiToken.issue!("agent")
    @garage = make_linked_project("garage", services: %w[app db], domains: %w[equipping.com],
                                  variables: [ { "name" => "RAILS_MASTER_KEY", "required" => true }, { "name" => "SENTRY_DSN", "required" => false } ])
    @garage.update!(domain_states: { "equipping.com" => { "state" => "DNS PENDING", "reason" => "not yet" } })
    @garage.secrets.create!(key: "RAILS_MASTER_KEY", value: "a-very-secret-master-key")
    make_deploy(@garage, 1, "go", sha: "a" * 40)
    make_deploy(@garage, 2, "no_go", sha: "b" * 40, error: "release hook failed (exit 3)", step: "Release", log: "one\ntwo\n")
    make_project("fresh")
  end

  def api(path) = get("/api/v1#{path}", headers: { "Authorization" => "Bearer #{@token}" })
  def json = response.parsed_body

  test "projects" do
    api "/projects"
    assert_response :success
    garage = json["projects"].find { |p| p["name"] == "garage" }
    assert_equal [ "no_go", "a" * 40, "garage.svnmns.com" ], garage.values_at("status", "running_sha", "host")
    assert_equal({ "equipping.com" => { "state" => "DNS PENDING", "reason" => "not yet" } }, garage["domains"])
    assert_equal [ 2, "no_go", "release hook failed (exit 3)" ], garage["last_deploy"].values_at("number", "status", "error")
    assert_equal "standby", json["projects"].find { |p| p["name"] == "fresh" }["status"]
  end

  test "a project" do
    api "/projects/garage"
    assert_response :success
    assert_equal [ "git@forgejo:houston/garage.git", "main", false ], json.values_at("repo_url", "branch", "webhook_verified")
    assert_equal({ "on" => "commit", "branch" => "main" }, json["deploy_rule"])
    assert_equal %w[app db], json["services"]
    assert_equal [ { "name" => "RAILS_MASTER_KEY", "required" => true, "set" => true }, { "name" => "SENTRY_DSN", "required" => false, "set" => false } ], json["secrets"]

    api "/projects/nope"
    assert_response :not_found
  end

  # docs/plans/app-stats.md, row 8.
  test "a project's stats" do
    api "/projects/garage"
    assert_nil json["stats"], "nothing sampled yet"

    stats = { "ID" => "w1", "Name" => "garage-web-#{"a" * 40}", "CPUPerc" => "50.00%", "MemUsage" => "256MiB / 1GiB" }.to_json
    fake = FakeDocker.new do |args|
      case args.first
      when "stats" then DockerCommand::Result.new(success: true, output: stats)
      when "inspect" then DockerCommand::Result.new(success: true, output: "w1 1073741824 0")
      when "system" then DockerCommand::Result.new(success: true, output: [ { "Name" => "garage_storage", "Size" => "2MB" } ].to_json)
      end
    end
    use_fake_docker(fake) { AppStats.sample! }
    api "/projects/garage"
    assert_equal({ "cpu_cores" => 0.5, "cpu_limit" => nil, "memory_bytes" => 256 * 1024**2, "memory_limit" => 1024**3, "disk_bytes" => 2_000_000 },
                 json["stats"].except("sampled_at"))
    assert json["stats"]["sampled_at"]
    api "/projects"
    assert_equal 0.5, json["projects"].find { |p| p["name"] == "garage" }.dig("stats", "cpu_cores")
  end

  test "reading never reveals secrets" do
    bodies = [ "/projects", "/projects/garage", "/projects/garage/deploys", "/projects/garage/deploys/2" ].map { |path| api(path); response.body }
    bodies.each do |body|
      assert_not_includes body, "a-very-secret-master-key"
      assert_not_includes body, "OPENSSH PRIVATE KEY"
      assert_not_includes body, WEBHOOK_SECRET
    end
  end

  test "deploys" do
    (3..25).each { |n| make_deploy(@garage, n, "go") }
    api "/projects/garage/deploys"
    assert_equal (6..25).to_a.reverse, json["deploys"].map { |d| d["number"] }
    api "/projects/garage/deploys?page=2"
    assert_equal [ 5, 4, 3, 2, 1 ], json["deploys"].map { |d| d["number"] }
    api "/projects/nope/deploys"
    assert_response :not_found
  end

  test "a deploy and its log" do
    api "/projects/garage/deploys/2"
    assert_response :success
    assert_equal [ 2, "no_go", "b" * 40, "release hook failed (exit 3)", "one\ntwo\n", 8 ], json.values_at("number", "status", "sha", "error", "log", "log_size")
    assert_equal({ "name" => "Release", "state" => "failed" }, json["steps"].find { |s| s["name"] == "Release" })

    api "/projects/garage/deploys/2?log_from=4"
    assert_equal "two\n", json["log"]
    api "/projects/garage/deploys/2?log_from=99"
    assert_equal "", json["log"]

    # One ASCII byte first, so a 256 KiB chunk from 0 ends inside an é, and
    # byte 2 is the middle of the first é.
    make_deploy(@garage, 3, "in_flight", log: "x" + ("é" * 200_000))
    api "/projects/garage/deploys/3?log_from=0"
    assert_operator json["log"].bytesize, :<, 256.kilobytes
    assert json["log"].valid_encoding?
    assert_equal json["log"].bytesize, json["log_next"]
    assert_equal 400_001, json["log_size"]
    api "/projects/garage/deploys/3?log_from=2"
    assert json["log"].start_with?("é")
    assert json["log"].valid_encoding?

    api "/projects/garage/deploys/99"
    assert_response :not_found
  end
end
