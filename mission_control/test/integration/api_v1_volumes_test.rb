require "test_helper"
require_relative "../support/project_helpers"

class ApiV1VolumesTest < ActionDispatch::IntegrationTest
  include ProjectHelpers

  setup do
    @token, = ApiToken.issue!("agent")
    @project = make_project("equip")
    @project.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" }, { "name" => "media", "path" => "/media" } ])
    @project.project_volumes.create!(name: "media", location: storage_locations(:unas), placed_at: Time.current)
  end

  def api(verb, path, body = nil)
    send(verb, "/api/v1#{path}", params: body&.to_json, headers: { "Authorization" => "Bearer #{@token}", "Content-Type" => "application/json" })
  end

  def json = response.parsed_body

  test "volumes over the API" do
    api :get, "/projects/equip/volumes"
    assert_equal [ { "name" => "storage", "path" => "/rails/storage", "location" => nil, "placed" => false },
                   { "name" => "media", "path" => "/media", "location" => "unas-nfs", "placed" => true } ], json["volumes"]

    api :patch, "/projects/equip/volumes/storage", { location: "unas-nfs" }
    assert_response :success
    assert_equal "unas-nfs", json["location"]
    api :patch, "/projects/equip/volumes/storage", { location: nil }
    assert_nil json["location"]

    api :patch, "/projects/equip/volumes/media", { location: nil }
    assert_response :conflict
    assert_match "already on unas-nfs", json["error"]

    api :patch, "/projects/equip/volumes/storage", { location: "nope" }
    assert_response :unprocessable_entity
    assert_match "no storage location nope", json["error"]

    api :patch, "/projects/equip/volumes/other", { location: nil }
    assert_response :not_found
  end
end
