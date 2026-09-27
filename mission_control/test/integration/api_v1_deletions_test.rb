require "test_helper"
require_relative "../support/project_helpers"

# Deleting a project over the API (docs/plans/delete-project.md, Batch 5):
# what houston delete calls.
class ApiV1DeletionsTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include ActiveJob::TestHelper

  setup do
    @token, = ApiToken.issue!("agent")
    @project = make_linked_project("equip")
  end

  def api(verb, path, body = nil, token: @token)
    headers = { "Content-Type" => "application/json" }
    headers["Authorization"] = "Bearer #{token}" if token
    send(verb, "/api/v1#{path}", params: body&.to_json, headers:)
  end

  def json = response.parsed_body

  test "deleting over the API" do
    assert_no_enqueued_jobs(only: DeleteProjectJob) { api :delete, "/projects/equip", { confirm: "Equip" } }
    assert_response :unprocessable_entity
    assert_match "type equip to confirm", json["error"]

    assert_enqueued_with(job: DeleteProjectJob) { api :delete, "/projects/equip", { confirm: "equip", delete_backups: true } }
    assert_response :accepted
    deletion = ProjectDeletion.sole
    assert_equal [ deletion.id, "equip", "queued", true ], json["deletion"].values_at("id", "name", "status", "delete_backups")
    assert_equal [ "token agent", true ], [ deletion.by, deletion.delete_backups ]

    api :get, "/projects/equip"
    assert_equal [ deletion.id, "queued" ], json["deleting"].values_at("id", "status")

    api :delete, "/projects/equip", { confirm: "equip" }
    assert_response :unprocessable_entity
    assert_match "equip is already being deleted", json["error"]

    deletion.update!(status: "go", step: "rows", snapshot_id: "5c5edd4c" + "0" * 56, snapshot_location: storage_locations(:unas), finished_at: Time.current, log: "GO: equip is deleted\n")
    @project.destroy!
    api :get, "/deletions/#{deletion.id}"
    assert_response :success
    assert_equal [ "go", "rows", "5c5edd4c" + "0" * 56, "unas-nfs", "git@forgejo:houston/equip.git", "GO: equip is deleted\n" ],
                 json.values_at("status", "step", "snapshot_id", "snapshot_location", "repo_url", "log")

    api :get, "/projects/equip"
    assert_response :not_found
  end

  test "a project that isn't being deleted says so" do
    api :get, "/projects/equip"
    assert_nil json["deleting"]
  end

  test "deleting, refused" do
    api :delete, "/projects/nope", { confirm: "nope" }
    assert_response :not_found
    assert_match "no project nope", json["error"]

    api :delete, "/projects/equip", { confirm: "equip" }, token: nil
    assert_response :unauthorized
    api :get, "/deletions/1", nil, token: nil
    assert_response :unauthorized
    assert_equal 0, ProjectDeletion.count

    api :get, "/deletions/999"
    assert_response :not_found
  end
end
