require "test_helper"
require_relative "../../support/project_helpers"
require_relative "../../support/fake_docker"

class Settings::StorageControllerTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeDockerHelper

  B2 = { kind: "b2", name: "b2-offsite", b2_bucket: "sevenmoons-houston", b2_key_id: "key-id", b2_application_key: "b2-secret-value" }.freeze

  setup { sign_in_as users(:one) }

  test "the storage list" do
    unas = storage_locations(:unas)
    unas.update!(prune_error: "Fatal: unable to create lock")
    project = make_project("equip")
    make_deploy(project, 1, "go")
    project.backup_runs.create!(location: unas, kind: "auto", reason: "manual", status: "go", heartbeat_at: Time.current, finished_at: Time.utc(2026, 9, 22, 3, 0))
    StorageLocation.create!(name: "b2-offsite", kind: "b2", settings: { "bucket" => "sm" }, restic_password: "pw", verified_at: Time.current, acknowledged_at: Time.current)

    get settings_storage_locations_path
    assert_response :success
    assert_select "nav.settings-nav a[href='#{settings_storage_locations_path}']", 0 # the current page isn't a link
    assert_select "[data-location=unas-nfs]", /DEFAULT.*nfs.*10\.0\.1\.20:\/volume1\/houston.*live volumes · backups.*1 project.*22 Sep 03:00.*Last prune failed: .*unable to create lock/m
    assert_select "[data-location=b2-offsite]", /b2.*backups.*not used yet/m
    assert_select "[data-location=b2-offsite]", { text: /live volumes/, count: 0 }
    assert_select "form[action='#{default_settings_storage_location_path("b2-offsite")}']"
  end

  test "adding a location" do
    use_fake_docker(FakeDocker.new) do |fake|
      post settings_storage_locations_path, params: { storage: B2 }
      assert_redirected_to settings_storage_location_path("b2-offsite")
      assert fake.calls.any? { |c| c.args.last == "init" }, "restic init ran"
      assert_not_includes fake.all_args.join(" "), "b2-secret-value"
    end
    location = StorageLocation.find_by!(name: "b2-offsite")
    assert location.verified?
    assert_not location.acknowledged?

    get settings_storage_location_path("b2-offsite")
    assert_select ".shown-once__password", location.restic_password
    assert_equal "no-store", response.headers["Cache-Control"]

    post acknowledge_settings_storage_location_path("b2-offsite"), params: { saved: "0" }
    assert_response :unprocessable_entity
    assert_not location.reload.acknowledged?

    post acknowledge_settings_storage_location_path("b2-offsite"), params: { saved: "1" }
    assert_redirected_to settings_storage_locations_path
    assert location.reload.acknowledged?
    assert_not location.default?, "a new location isn't the default"
    assert storage_locations(:unas).reload.default?

    get settings_storage_location_path("b2-offsite")
    assert_redirected_to settings_storage_locations_path
    follow_redirect!
    assert_not_includes response.body, location.restic_password

    use_fake_docker(FakeDocker.new) do
      post settings_storage_locations_path, params: { storage: B2 }
      assert_response :unprocessable_entity
      assert_select ".field__error", /already used/
      post settings_storage_locations_path, params: { storage: B2.merge(name: "Bad Name") }
      assert_response :unprocessable_entity
    end
  end

  test "making a location the default" do
    offsite = StorageLocation.create!(name: "b2-offsite", kind: "b2", settings: { "bucket" => "sm" }, restic_password: "pw", verified_at: Time.current, acknowledged_at: Time.current)
    later = StorageLocation.create!(name: "later", kind: "b2", settings: { "bucket" => "sm2" }, restic_password: "pw", verified_at: Time.current)
    project = make_project("equip")

    post default_settings_storage_location_path("later")
    assert_redirected_to settings_storage_locations_path
    assert storage_locations(:unas).reload.default?

    post default_settings_storage_location_path("b2-offsite")
    assert_redirected_to settings_storage_locations_path
    assert offsite.reload.default?
    assert_not storage_locations(:unas).reload.default?
    assert_equal offsite, project.backup_location
    assert later
  end

  test "storage needs the admin" do
    delete session_path
    get settings_storage_locations_path
    assert_redirected_to new_session_path
  end
end
