require "test_helper"

class DeployTest < ActiveSupport::TestCase
  # The transaction decides in the normal case; the indexes hold if two
  # requests ever race past it.
  test "the database allows one deploy in flight and unique numbers" do
    equip = Project.create!(name: "equip", app_service: "app", services: %w[app], health: "/up", port: 80)
    other = Project.create!(name: "other", app_service: "app", services: %w[app], health: "/up", port: 80)
    row = ->(project, number, status) { { project_id: project.id, number:, sha: "a" * 40, ref: "refs/heads/main", status:, token_digest: "d", heartbeat_at: Time.current } }

    Deploy.insert!(row.(equip, 1, "in_flight"))
    assert_raises(ActiveRecord::RecordNotUnique) { Deploy.insert!(row.(equip, 2, "in_flight")) }
    assert_raises(ActiveRecord::RecordNotUnique) { Deploy.insert!(row.(equip, 1, "go")) }
    Deploy.insert!(row.(equip, 2, "go"))
    Deploy.insert!(row.(other, 1, "in_flight"))
    assert_equal 3, Deploy.count
  end
end
