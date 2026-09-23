require "test_helper"
require_relative "../support/project_helpers"

class ApiV1StorageTest < ActionDispatch::IntegrationTest
  include ProjectHelpers

  setup do
    @token, = ApiToken.issue!("agent")
    storage_locations(:unas).update!(restic_password: "restic-password-xyz")
    @offsite = StorageLocation.create!(name: "b2-offsite", kind: "b2", settings: { "bucket" => "sm" }, credentials: { "key_id" => "k", "application_key" => "b2-secret-value" },
                                       restic_password: "offsite-password-xyz", verified_at: Time.current, acknowledged_at: Time.current)
    @project = make_project("equip")
  end

  def api(verb, path, body = nil)
    send(verb, "/api/v1#{path}", params: body&.to_json, headers: { "Authorization" => "Bearer #{@token}", "Content-Type" => "application/json" })
  end

  def json = response.parsed_body

  test "storage over the API" do
    api :get, "/storage"
    assert_response :success
    assert_equal %w[b2-offsite unas-nfs], json["locations"].map { |l| l["name"] }
    unas = json["locations"].find { |l| l["name"] == "unas-nfs" }
    assert_equal [ "nfs", "10.0.1.20:/volume1/houston", true, true, true ], unas.values_at("kind", "where", "live", "default", "confirmed")
    %w[restic-password-xyz offsite-password-xyz b2-secret-value].each { |secret| assert_not_includes response.body, secret }

    api :patch, "/storage/b2-offsite", { default: true }
    assert_response :success
    assert @offsite.reload.default?

    api :patch, "/storage/nope", { default: true }
    assert_response :not_found

    api :patch, "/projects/equip/backup_target", { location: "unas-nfs" }
    assert_response :success
    assert_equal "unas-nfs", json["backup_location"]
    assert_equal storage_locations(:unas), @project.reload.backup_location

    api :patch, "/projects/equip/backup_target", { location: nil }
    assert_equal "b2-offsite", json["backup_location"], "back to the default"

    api :patch, "/projects/equip/backup_target", { location: "nope" }
    assert_response :unprocessable_entity
  end
end
