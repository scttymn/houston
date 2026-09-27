require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"

# The garbage collection queued after a deletion (docs/plans/delete-project.md,
# Batch 7): it waits out pushes, and says how it went in the deletion's log.
class RegistryCleanupJobTest < ActiveJob::TestCase
  include ProjectHelpers
  include FakeDockerHelper

  setup do
    @deletion = ProjectDeletion.create!(name: "equip", by: "admin", status: "go", log: "GO: equip is deleted\n", heartbeat_at: Time.current)
  end

  def registry(gc = DockerCommand::Result.new(success: true, output: "12 blobs marked, 7 blobs and 0 manifests eligible for deletion\n"))
    FakeDocker.new do |args, _env|
      if args[0..1] == %w[ps -q] then DockerCommand::Result.new(success: true, output: "5e61571f0001\n")
      elsif args[0] == "exec" then gc
      end
    end
  end

  test "frees the space and says so" do
    use_fake_docker(registry) { RegistryCleanupJob.perform_now(@deletion) }
    assert_equal "GO: equip is deleted\nok  registry space freed (7 blobs and 0 manifests eligible for deletion)\n", @deletion.reload.log
  end

  test "waits while an image may be pushed" do
    Deploy.start!(make_project("equip-x"), sha: "a" * 40, ref: "refs/heads/main")
    fake = registry
    assert_enqueued_with(job: RegistryCleanupJob, args: [ @deletion ]) { use_fake_docker(fake) { RegistryCleanupJob.perform_now(@deletion) } }
    assert_empty fake.calls
    assert_equal "GO: equip is deleted\n", @deletion.reload.log
  end

  test "a failure is said, and changes nothing else" do
    use_fake_docker(registry(failure("level=fatal msg=\"failed to garbage collect\""))) { RegistryCleanupJob.perform_now(@deletion) }
    @deletion.reload
    assert_match "registry space wasn't freed: level=fatal", @deletion.log
    assert_equal "go", @deletion.status
    assert_not RegistryCleanup.running?
  end
end
