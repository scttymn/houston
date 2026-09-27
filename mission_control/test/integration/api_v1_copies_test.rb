require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_git"
require_relative "../support/handover_helpers"

# Copying a project over the API (docs/plans/copy-project.md, Batch 5): what
# houston copy calls.
class ApiV1CopiesTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeGitHelper
  include HandoverHelpers
  include ActiveJob::TestHelper

  HEAD = "c0bb1e5" + "0" * 33

  setup do
    @token, = ApiToken.issue!("agent")
    @old = make_linked_project("equip-go")
    make_deploy(@old, 1, "go")
    make_deploy(@old, 2, "hold", sha: HEAD).update!(proposed_name: "equip")
  end

  def repo
    sync = { name: "equip", app_service: "app", services: %w[app], domains: [], variables: [], health: "/up", port: 80, deploy_rule: { on: "commit", branch: "main" } }.deep_stringify_keys
    FakeGit.new { |args| args.include?("rev-parse") ? git_ok("#{HEAD}\n") : (args.include?("inspect") ? git_ok({ "sync" => sync }.to_json) : nil) }
  end

  def api(verb, path, body = nil, token: @token)
    headers = { "Content-Type" => "application/json" }
    headers["Authorization"] = "Bearer #{token}" if token
    use_fake_git(repo) { send(verb, "/api/v1#{path}", params: body&.to_json, headers:) }
  end

  def json = response.parsed_body

  test "copying over the API" do
    api :get, "/projects/equip-go"
    assert_equal({ "name" => "equip", "sha" => HEAD, "deploy" => 2, "refusal" => nil }, json["copy_proposal"])

    api :post, "/projects/equip-go/copy", { confirm: "nope" }
    assert_response :unprocessable_entity
    assert_match "type equip-go to confirm", json["error"]

    api :post, "/projects/equip-go/copy", { confirm: "equip-go" }
    assert_response :accepted
    copy = ProjectCopy.sole
    assert_equal [ copy.id, "equip-go", "equip", "queued", 1, HEAD, "token agent" ], json["copy"].values_at("id", "from", "to", "status", "deploy", "sha", "by")

    api :post, "/projects/equip/copy/cancel"
    assert_response :success
    assert_equal [ "no_go", "cancelled by token agent" ], json["copy"].values_at("status", "error")
    api :post, "/projects/equip/copy/cancel"
    assert_response :unprocessable_entity
    assert_match "is done", json["error"]
  end

  test "undoing over the API" do
    api :post, "/projects/equip-go/copy", { confirm: "equip-go" }
    copy = ProjectCopy.sole
    copy.update!(status: "go", handed_over: [], handed_over_at: Time.current)
    copy.deploy.update!(status: "go")

    api :post, "/projects/equip/copy/undo", { confirm: "nope" }
    assert_response :unprocessable_entity
    with_handover(handover_doing) { api :post, "/projects/equip/copy/undo", { confirm: "equip" } }
    assert_response :accepted
    assert_equal [ "equip", false ], json["deletion"].values_at("name", "delete_backups")
  end

  test "refused" do
    api :post, "/projects/nope/copy", { confirm: "nope" }
    assert_response :not_found
    api :post, "/projects/equip-go/copy/cancel"
    assert_response :not_found
    assert_match "equip-go isn't a copy", json["error"]
    api :post, "/projects/equip-go/copy", { confirm: "equip-go" }, token: nil
    assert_response :unauthorized
    assert_equal 0, ProjectCopy.count
  end
end
