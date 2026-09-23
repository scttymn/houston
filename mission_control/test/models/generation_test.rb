require "test_helper"
require_relative "../support/project_helpers"

# A project's Docker names in one data generation (docs/plans/restore.md,
# Batch 3), matching Go's kamal.Names.
class GenerationTest < ActiveSupport::TestCase
  include ProjectHelpers

  test "the names" do
    project = make_project("equip", services: %w[app db])
    one, two = Generation.new(project, 1), Generation.new(project, 2)
    assert_equal [ "equip_storage", "equip.g2_storage" ], [ one.volume("storage"), two.volume("storage") ]
    assert_equal [ "equip-db", "equip-db-g2" ], [ one.container("db"), two.container("db") ]
    assert_equal [ "volumes/equip/storage", "volumes/equip.g2/storage" ], [ one.directory("storage"), two.directory("storage") ]
    assert_equal 1, project.data_generation, "every project starts at generation 1"
    assert_equal Generation.new(project, 1).volume("x"), project.generation.volume("x")
  end

  test "container names are claimed per generation" do
    project = make_project("equip", services: %w[app db])
    assert_equal %w[equip equip-db], project.host_names
    assert_equal %w[equip equip-db-g2], project.host_names(generation: 2)

    # A project named like generation 2's container: the claim clashes.
    Project.create!(name: "equip-db-g2", app_service: "app", services: %w[app], health: "/up", port: 80).hosts.create!(name: "equip-db-g2")
    assert_raises(ActiveRecord::RecordNotUnique) { project.hosts.create!(name: "equip-db-g2") }
  end
end
