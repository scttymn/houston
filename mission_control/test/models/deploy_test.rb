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

  test "one queued deploy per project" do
    equip = Project.create!(name: "equip", app_service: "app", services: %w[app], health: "/up", port: 80)
    other = Project.create!(name: "other", app_service: "app", services: %w[app], health: "/up", port: 80)
    row = ->(project, number) { { project_id: project.id, number:, sha: "a" * 40, ref: "refs/heads/main", status: "queued", token_digest: "", heartbeat_at: Time.current } }

    Deploy.insert!(row.(equip, 1))
    assert_raises(ActiveRecord::RecordNotUnique) { Deploy.insert!(row.(equip, 2)) }
    Deploy.insert!(row.(other, 1))
  end

  # Two runners can pick the same queued deploy; the conditional flip lets
  # exactly one of them have it.
  test "a deploy is claimed once" do
    project = Project.create!(name: "equip", app_service: "app", services: %w[app], health: "/up", port: 80)
    queued = Deploy.queue!(project, sha: "a" * 40, ref: "refs/heads/main")
    seen_by_b = Deploy.find(queued.id)

    deploy, token = Deploy.claim!(queued, runner: "houston-runner-1")
    assert deploy
    assert deploy.owned_by?(token)

    assert_nil Deploy.claim!(seen_by_b, runner: "houston-runner-2")
    assert_equal "houston-runner-1", queued.reload.runner
    assert queued.owned_by?(token)
  end

  test "a deploy records the generation it deploys" do
    equip = Project.create!(name: "equip", app_service: "app", services: %w[app], health: "/up", port: 80, data_generation: 3)
    deploy, = Deploy.start!(equip, sha: "a" * 40, ref: "refs/heads/main")
    assert_equal 3, deploy.generation
    equip.update!(data_generation: 4)
    queued = Deploy.queue!(equip, sha: "b" * 40, ref: "refs/heads/main")
    deploy.update!(status: "go", finished_at: Time.current)
    claimed, = Deploy.claim!(queued, runner: "houston-runner-1")
    assert_equal 4, claimed.generation, "a queued deploy takes the generation when it's claimed"
  end

  test "every deploy is a deploy until a restore says otherwise" do
    equip = Project.create!(name: "equip", app_service: "app", services: %w[app], health: "/up", port: 80)
    deploy, = Deploy.start!(equip, sha: "a" * 40, ref: "refs/heads/main")
    assert_equal "deploy", deploy.kind
    assert_nil deploy.source_snapshot_id
    assert_equal "backup", BackupRun.new.operation
  end
end
