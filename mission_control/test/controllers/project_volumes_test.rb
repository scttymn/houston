require "test_helper"
require_relative "../support/project_helpers"

class ProjectVolumesTest < ActionDispatch::IntegrationTest
  include ProjectHelpers

  setup do
    @project = make_project("equip")
    @project.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" }, { "name" => "media", "path" => "/media" } ])
  end

  test "choosing where a volume lives" do
    sign_in_as users(:one)
    @project.project_volumes.create!(name: "media", placed_at: Time.current)
    get project_path("equip")
    assert_select ".volumes [data-volume=storage] select[name=location] option", 2 # local disk, unas-nfs
    assert_select ".volumes [data-volume=media]", /local disk.*moving a volume is a later feature/m
    assert_select ".volumes [data-volume=media] select", 0

    patch project_volume_path("equip", "storage"), params: { location: "unas-nfs" }
    assert_redirected_to project_path("equip")
    assert_equal storage_locations(:unas), @project.project_volumes.find_by!(name: "storage").location
    follow_redirect!
    assert_select ".volumes [data-volume=storage]", /Live SQLite over a network share risks corruption/

    patch project_volume_path("equip", "media"), params: { location: "unas-nfs" }
    assert_redirected_to project_path("equip")
    follow_redirect!
    assert_select ".notice--nogo", /media is already on local disk/
    assert_nil @project.project_volumes.find_by!(name: "media").location
  end

  test "volumes need the admin" do
    patch project_volume_path("equip", "storage"), params: { location: "unas-nfs" }
    assert_redirected_to sign_in_path
    assert_equal 0, ProjectVolume.count
  end
end
