# Frees the space of deleted manifests in Houston's registry: its own
# garbage collection (docs/plans/delete-project.md, Batch 7). That must never
# run during a push, or it can delete a layer being uploaded. Only deploys
# and restores push, so it runs only while none is in flight, holding the
# lock (Installation#registry_cleanup_since) that keeps new ones from
# starting until it's done.
module RegistryCleanup
  class Busy < StandardError; end
  class Failed < StandardError; end

  # Mission Control stopped while it ran: after this, the lock is forgotten.
  STALE_AFTER = 30.minutes
  TIMEOUT = 30.minutes

  def self.running? = Installation.where(registry_cleanup_since: STALE_AFTER.ago..).exists?

  # Runs the garbage collection; returns what it freed, in the registry's
  # words (or nil). Busy while an image may be pushed or it already runs.
  def self.run!
    take_lock!
    begin
      registry = Registry.new
      id = registry.container or raise Failed, "no registry container (the #{Registry.compose_project} project's registry service) is running"
      ran = DockerCommand.run("exec", id, "registry", "garbage-collect", Registry::CONFIG, timeout: TIMEOUT.to_i)
      raise Failed, ran.output.to_s.lines.last(3).join.strip.truncate(1000) unless ran.success
      ran.output.to_s[/\d+ blobs and \d+ manifests eligible for deletion/]
    ensure
      Installation.update_all(registry_cleanup_since: nil)
    end
  end

  def self.take_lock!
    Installation.transaction do
      if (deploy = Deploy.in_flight.where(heartbeat_at: Deploy::STALE_AFTER.ago..).order(:id).first)
        raise Busy, "#{deploy.restore? ? "restore" : "deploy"} ##{deploy.number} of #{deploy.project.name} is in flight"
      end
      taken = Installation.where(registry_cleanup_since: nil).or(Installation.where(registry_cleanup_since: ...STALE_AFTER.ago))
                          .update_all(registry_cleanup_since: Time.current)
      raise Busy, "the registry is already being cleaned" unless taken.positive?
    end
  end
  private_class_method :take_lock!
end
