require "test_helper"
require_relative "../support/api_helpers"

class ApiDeploysTest < ActionDispatch::IntegrationTest
  include ApiHelpers

  SHA = "0123456789abcdef0123456789abcdef01234567"

  setup do
    @project = Project.create!(name: "equip", app_service: "app", services: %w[app], health: "/up", port: 80)
  end

  def start(name: "equip", sha: SHA, ref: "refs/heads/main", headers: api_headers)
    post "/api/projects/#{name}/deploys", params: { sha:, ref: }.to_json, headers:
  end

  def report(deploy_id, token, body, headers: {})
    patch "/api/deploys/#{deploy_id}", params: body.to_json, headers: api_headers(**headers).merge("X-Houston-Deploy-Token" => token.to_s)
  end

  def started
    start
    assert_response :created
    [ json["id"], json["token"] ]
  end

  test "a deploy gets the next number and a token" do
    start
    assert_response :created
    assert_equal 1, json["number"]
    assert_nil json["took_over"]
    assert_operator json["token"].length, :>=, 43
    deploy = Deploy.find(json["id"])
    assert_equal [ "in_flight", SHA, "refs/heads/main" ], [ deploy.status, deploy.sha, deploy.ref ]
    assert_not_equal json["token"], deploy.token_digest
    assert_not_includes deploy.token_digest, json["token"]

    report(deploy.id, json["token"], { status: "go" })
    assert_response :success
    start
    assert_equal 2, json["number"]
  end

  test "a deploy needs a synced project and a real sha" do
    start(name: "nope")
    assert_response :not_found

    [ { sha: "abc123" }, { sha: SHA.upcase }, { sha: "#{SHA}0" }, { ref: "" }, { ref: "r" * 256 } ].each do |bad|
      start(**bad)
      assert_response :unprocessable_entity, bad.inspect
    end
    assert_equal 0, Deploy.count
  end

  test "one deploy in flight per project" do
    started
    start
    assert_response :conflict
    assert_match(/#1 is in flight/, json["error"])
    assert_equal 1, Deploy.count
  end

  test "a silent deploy is taken over" do
    first_id, = started
    travel 3.minutes do
      start
      assert_response :created
      assert_equal [ 2, 1 ], [ json["number"], json["took_over"] ]
      first = Deploy.find(first_id)
      assert_equal "no_go", first.status
      assert_match(/abandoned/, first.error)
      assert first.finished_at
    end
  end

  test "only the deploy's owner reports on it" do
    id, token = started
    other = Project.create!(name: "other", app_service: "app", services: %w[app], health: "/up", port: 80)
    post "/api/projects/other/deploys", params: { sha: SHA, ref: "refs/heads/main" }.to_json, headers: api_headers
    other_token = json["token"]

    [ nil, "", "wrong", other_token ].each do |bad|
      report(id, bad, { step: "hacked", log: "x", status: "go" })
      assert_response :forbidden, bad.inspect
    end
    report(id, token, { status: "go" }, headers: { "Cf-Ray" => "8a1b2c3d-MCI" })
    assert_response :not_found

    deploy = Deploy.find(id)
    assert_equal [ "in_flight", nil, "" ], [ deploy.status, deploy.step, deploy.log ]
    assert other
  end

  test "progress appends to the log" do
    id, token = started
    before = Deploy.find(id).heartbeat_at

    travel 20.seconds do
      report(id, token, { step: "Building", log: "step 1\n" })
      assert_response :success
      report(id, token, { log: "step 2\n" })
      assert_response :success
    end

    deploy = Deploy.find(id)
    assert_equal [ "Building", "step 1\nstep 2\n" ], [ deploy.step, deploy.log ]
    assert_operator deploy.heartbeat_at, :>, before
  end

  test "a finished deploy doesn't change" do
    id, token = started
    report(id, token, { status: "go", log: "done\n" })
    assert_response :success
    deploy = Deploy.find(id)
    assert deploy.finished_at

    report(id, token, { status: "no_go", step: "again", log: "more" })
    assert_response :conflict
    assert_equal [ "go", "done\n", nil ], Deploy.find(id).then { |d| [ d.status, d.log, d.step ] }
  end

  test "a taken-over deploy can't finish" do
    id, token = started
    travel 3.minutes do
      start
      assert_response :created
      report(id, token, { status: "go", log: "I'm still here\n" })
      assert_response :conflict
      assert_match(/taken over|no longer in flight/, json["error"])
      old = Deploy.find(id)
      assert_equal "no_go", old.status
      assert_not_includes old.log, "still here"
    end
  end

  test "the log is capped" do
    id, token = started
    report(id, token, { log: "x" * (256.kilobytes + 1) })
    assert_response :content_too_large

    17.times { report(id, token, { log: "y" * 250.kilobytes }) }
    report(id, token, { log: "after the cap\n", status: "no_go", error: "release failed" })
    assert_response :success

    deploy = Deploy.find(id)
    assert_operator deploy.log.bytesize, :<=, 4.megabytes + 200
    assert_equal 1, deploy.log.scan("[log truncated").size
    assert_not_includes deploy.log, "after the cap"
    assert_equal [ "no_go", "release failed" ], [ deploy.status, deploy.error ]
  end

  test "progress is validated" do
    id, token = started
    [ { status: "done" }, { status: "in_flight" }, { step: "s" * 101 }, { error: "e" * 1001 }, { log: 42 } ].each do |bad|
      report(id, token, bad)
      assert_response :unprocessable_entity, bad.inspect
    end
    deploy = Deploy.find(id)
    assert_equal [ "in_flight", nil, "", nil ], [ deploy.status, deploy.step, deploy.log, deploy.error ]
  end
end
