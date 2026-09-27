require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"

# Houston's registry after a project is deleted (docs/plans/delete-project.md,
# Batch 7): its images' manifests deleted, then the space freed by garbage
# collection, never while an image is being pushed.
class RegistryCleanupTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeDockerHelper

  REGISTRY_ID = "5e61571f0001"

  def registry(overrides = {})
    FakeDocker.new do |args, _env|
      hit = overrides.find { |matcher, _| matcher.call(args) }
      next hit.last if hit

      if args[0..1] == %w[ps -q] && args.include?("label=com.docker.compose.service=registry")
        DockerCommand::Result.new(success: true, output: "#{REGISTRY_ID}\n")
      elsif args[0] == "exec" && args.include?("garbage-collect")
        DockerCommand::Result.new(success: true, output: "equip: marking manifest sha256:d1\n12 blobs marked, 7 blobs and 0 manifests eligible for deletion\n")
      end
    end
  end

  test "garbage collection in the registry's container, with the lock held and let go" do
    fake = registry
    summary = use_fake_docker(fake) { RegistryCleanup.run! }

    assert_equal "7 blobs and 0 manifests eligible for deletion", summary
    assert_equal [ %w[ps -q --filter label=com.docker.compose.project=houston --filter label=com.docker.compose.service=registry],
                   [ "exec", REGISTRY_ID, "registry", "garbage-collect", "/etc/distribution/config.yml" ] ], fake.calls.map(&:args)
    assert_not RegistryCleanup.running?, "the lock is let go"
  end

  test "the lock is held while it runs, and let go when it fails" do
    during = nil
    fake = FakeDocker.new do |args, _env|
      if args[0] == "exec"
        during = RegistryCleanup.running?
        failure("level=fatal msg=\"failed to garbage collect: filesystem: Path not found\"")
      elsif args[0..1] == %w[ps -q]
        DockerCommand::Result.new(success: true, output: "#{REGISTRY_ID}\n")
      end
    end
    error = assert_raises(RegistryCleanup::Failed) { use_fake_docker(fake) { RegistryCleanup.run! } }
    assert_match "Path not found", error.message
    assert during, "held while garbage-collect ran"
    assert_not RegistryCleanup.running?

    no_registry = FakeDocker.new { |args, _| DockerCommand::Result.new(success: true, output: "") if args[0..1] == %w[ps -q] }
    assert_match "no registry container", assert_raises(RegistryCleanup::Failed) { use_fake_docker(no_registry) { RegistryCleanup.run! } }.message
    assert_not RegistryCleanup.running?
  end

  test "never while an image may be pushed" do
    project = make_project("equip-x")
    deploy, = Deploy.start!(project, sha: "a" * 40, ref: "refs/heads/main")
    fake = registry
    error = assert_raises(RegistryCleanup::Busy) { use_fake_docker(fake) { RegistryCleanup.run! } }
    assert_match "deploy #1 of equip-x is in flight", error.message
    assert_empty fake.calls
    assert_not RegistryCleanup.running?

    # A deploy gone silent (its runner died) doesn't hold it back.
    deploy.update!(heartbeat_at: (Deploy::STALE_AFTER + 1.second).ago)
    use_fake_docker(fake) { RegistryCleanup.run! }
    assert fake.calls.any? { |c| c.args.include?("garbage-collect") }
  end

  test "one at a time; a stale lock is taken over" do
    Installation.current.update!(registry_cleanup_since: 1.minute.ago)
    assert RegistryCleanup.running?
    assert_match "already", assert_raises(RegistryCleanup::Busy) { use_fake_docker(registry) { RegistryCleanup.run! } }.message

    Installation.current.update!(registry_cleanup_since: (RegistryCleanup::STALE_AFTER + 1.minute).ago)
    assert_not RegistryCleanup.running?
    use_fake_docker(registry) { RegistryCleanup.run! }
    assert_nil Installation.current.reload.registry_cleanup_since
  end

  test "while it runs, no image is pushed" do
    project = make_project("equip-x")
    Deploy.queue!(project, sha: "a" * 40, ref: "refs/heads/main")
    Installation.current.update!(registry_cleanup_since: Time.current)

    assert_nil Deploy.claim_next!(runner: "houston-runner-1")
    error = assert_raises(Deploy::RegistryBusy) { Deploy.start!(make_project("other"), sha: "b" * 40, ref: "refs/heads/main") }
    assert_match "Houston is cleaning its registry; try again in a minute", error.message

    Installation.current.update!(registry_cleanup_since: nil)
    assert Deploy.claim_next!(runner: "houston-runner-1")
  end

  test "an app's images in the registry" do
    tags = stub_request(:get, "http://registry:5000/v2/equip/tags/list").to_return(status: 200, body: { name: "equip", tags: %w[aaaa latest bbbb] }.to_json)
    %w[aaaa latest].each { |t| stub_request(:head, "http://registry:5000/v2/equip/manifests/#{t}").to_return(status: 200, headers: { "Docker-Content-Digest" => "sha256:d1" }) }
    stub_request(:head, "http://registry:5000/v2/equip/manifests/bbbb").to_return(status: 200, headers: { "Docker-Content-Digest" => "sha256:d2" })
    stub_request(:delete, "http://registry:5000/v2/equip/manifests/sha256:d1").to_return(status: 202)
    stub_request(:delete, "http://registry:5000/v2/equip/manifests/sha256:d2").to_return(status: 404, body: { errors: [ { code: "MANIFEST_UNKNOWN" } ] }.to_json)

    assert_equal 2, Registry.new.delete_repository("equip")
    assert_requested tags
    assert_requested :head, "http://registry:5000/v2/equip/manifests/aaaa",
                     headers: { "Accept" => Registry::MANIFEST_TYPES.join(", ") }
    assert_requested :delete, "http://registry:5000/v2/equip/manifests/sha256:d1", times: 1

    stub_request(:get, "http://registry:5000/v2/gone/tags/list").to_return(status: 404, body: { errors: [ { code: "NAME_UNKNOWN" } ] }.to_json)
    assert_equal 0, Registry.new.delete_repository("gone")
    stub_request(:get, "http://registry:5000/v2/none/tags/list").to_return(status: 200, body: { name: "none", tags: nil }.to_json)
    assert_equal 0, Registry.new.delete_repository("none")

    stub_request(:delete, "http://registry:5000/v2/equip/manifests/sha256:d1").to_return(status: 405, body: { errors: [ { code: "UNSUPPORTED", message: "The operation is unsupported." } ] }.to_json)
    assert_match "405: UNSUPPORTED", assert_raises(Registry::Error) { Registry.new.delete_repository("equip") }.message
  end

  test "whether the registry allows deletes" do
    env = ->(list) { registry(->(a) { a[0] == "inspect" } => DockerCommand::Result.new(success: true, output: list.to_json)) }
    assert use_fake_docker(env.([ "PATH=/bin", "REGISTRY_STORAGE_DELETE_ENABLED=true" ])) { Registry.new.deletes_enabled? }
    assert_not use_fake_docker(env.([ "PATH=/bin" ])) { Registry.new.deletes_enabled? }
    assert_not use_fake_docker(FakeDocker.new { |a, _| DockerCommand::Result.new(success: true, output: "") if a[0..1] == %w[ps -q] }) { Registry.new.deletes_enabled? }
  end
end
