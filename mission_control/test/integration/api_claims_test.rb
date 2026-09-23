require "test_helper"
require_relative "../support/api_helpers"
require_relative "../support/project_helpers"

class ApiClaimsTest < ActionDispatch::IntegrationTest
  include ApiHelpers
  include ProjectHelpers

  setup do
    @known_hosts = Tempfile.new("known_hosts")
    @host_key = RepoLink.generate_key.last.split[0, 2].join(" ")
    @known_hosts.write("forgejo #{@host_key}\ngithub.com #{RepoLink.generate_key.last.split[0, 2].join(" ")}\n")
    @known_hosts.flush
    ENV["HOUSTON_KNOWN_HOSTS"] = @known_hosts.path
  end

  teardown do
    ENV.delete("HOUSTON_KNOWN_HOSTS")
    @known_hosts.close!
  end

  def claim(runner: "houston-runner-1", wait: 0, headers: api_headers)
    post "/api/runner/jobs/claim", params: { runner:, wait: }.to_json, headers:
  end

  test "a runner claims the oldest queued deploy" do
    older = make_linked_project("garage")
    newer = make_linked_project("rideclub")
    travel(-1.minute) { Deploy.queue!(older, sha: "a" * 40, ref: "refs/heads/main") }
    Deploy.queue!(newer, sha: "b" * 40, ref: "refs/heads/main")

    claim
    assert_response :success
    deploy = json["deploy"]
    assert_equal [ 1, "a" * 40, "refs/heads/main", nil ], deploy.values_at("number", "sha", "ref", "took_over")
    assert_operator deploy["token"].length, :>=, 43
    assert_equal({ "name" => "garage", "repo_url" => older.repo_url, "branch" => "main", "compose_path" => "compose.yml",
                   "deploy_key" => older.deploy_key_private }, json["project"])
    assert_equal [ "forgejo #{@host_key}" ], json["known_hosts"]

    record = Deploy.find(deploy["id"])
    assert_equal [ "in_flight", "houston-runner-1" ], [ record.status, record.runner ]
    assert record.owned_by?(deploy["token"])
    assert_in_delta Time.current, record.heartbeat_at, 5

    claim(runner: "houston-runner-2")
    assert_equal "rideclub", json.dig("project", "name")
  end

  test "nothing to claim" do
    claim
    assert_response :no_content

    busy = make_linked_project("garage")
    make_deploy(busy, 1, "in_flight", started: 10.seconds.ago)
    Deploy.queue!(busy, sha: "b" * 40, ref: "refs/heads/main")
    claim
    assert_response :no_content, "garage already has a deploy in flight"

    other = make_linked_project("rideclub")
    Deploy.queue!(other, sha: "c" * 40, ref: "refs/heads/main")
    claim
    assert_equal "rideclub", json.dig("project", "name")
  end

  test "a silent runner's deploy is taken over at claim" do
    project = make_linked_project("garage")
    silent = make_deploy(project, 1, "in_flight", started: 3.minutes.ago)
    Deploy.queue!(project, sha: "b" * 40, ref: "refs/heads/main")

    claim
    assert_response :success
    assert_equal [ 2, 1 ], json["deploy"].values_at("number", "took_over")
    assert_equal "no_go", silent.reload.status
    assert_match(/abandoned/, silent.error)
  end

  test "claims are guarded" do
    Deploy.queue!(make_linked_project("garage"), sha: "a" * 40, ref: "refs/heads/main")

    [ { runner: "evil" }, { runner: "houston-runner-" }, { wait: 26 }, { wait: -1 } ].each do |bad|
      claim(**bad)
      assert_response :unprocessable_entity, bad.inspect
    end
    claim(headers: api_headers(token: "wrong"))
    assert_response :unauthorized
    claim(headers: api_headers("Cf-Ray" => "8a1b-MCI"))
    assert_response :not_found
    assert_equal "queued", Deploy.sole.status
    assert_equal 0, Runner.count
  end
end
