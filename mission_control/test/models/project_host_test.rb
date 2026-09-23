require "test_helper"

class ProjectHostTest < ActiveSupport::TestCase
  # The unique index, not a Ruby check, is what stops two syncs that race.
  test "a host name has one owner" do
    shop = Project.create!(name: "shop", app_service: "app", services: %w[app db], health: "/up", port: 80)
    other = Project.create!(name: "other", app_service: "app", services: %w[app], health: "/up", port: 80)
    shop.hosts.create!(name: "shop-db")

    assert_raises(ActiveRecord::RecordNotUnique) do
      ProjectHost.insert!({ project_id: other.id, name: "shop-db" })
    end
  end
end
