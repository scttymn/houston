require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/fake_git"
require_relative "../support/backup_helpers"

class ApiV1RestoresTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeDockerHelper
  include FakeGitHelper
  include BackupHelpers

  OLD = "d4e0b17" + "0" * 33

  setup do
    @token, = ApiToken.issue!("agent")
    @project = make_backup_project
    @project.update!(repo_url: "git@forgejo:houston/equip.git", deploy_key_private: "key\n")
  end

  def api(verb, path, body)
    listing = FakeDocker.new { DockerCommand::Result.new(success: true, output: [ snapshot_json(id: "33333333", time: "2026-09-22T03:00:00Z", sha: OLD) ].to_json) }
    git = FakeGit.new { |args| args.include?("rev-parse") ? git_ok("#{OLD}\n") : git_ok }
    use_fake_git(git) { use_fake_docker(listing) { send(verb, "/api/v1#{path}", params: body.to_json, headers: { "Authorization" => "Bearer #{@token}", "Content-Type" => "application/json" }) } }
  end

  def json = response.parsed_body

  test "restoring over the API" do
    api :post, "/projects/equip/restores", { snapshot: "33333333", confirm: "nope" }
    assert_response :unprocessable_entity
    assert_match "type equip to confirm", json["error"]

    api :post, "/projects/equip/restores", { snapshot: "33333333", confirm: "equip" }
    assert_response :accepted
    assert_equal [ 2, "queued", "restore", OLD ], json.values_at("number", "status", "kind", "sha")

    api :post, "/projects/equip/restores", { snapshot: "33333333", confirm: "equip" }
    assert_response :unprocessable_entity
    assert_match "wait for #2", json["error"]
  end
end
