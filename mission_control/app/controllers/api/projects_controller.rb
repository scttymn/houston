class Api::ProjectsController < Api::BaseController
  # POST /api/projects/sync. Saves the project first, so the admin can fill in
  # missing secrets even when the answer is HOLD; DNS is ensured on every
  # sync, so a retry after a Cloudflare failure finishes the job.
  # Room for a 512 KB maintenance page after JSON escapes its < > & to six
  # bytes each (about 3 MB at worst), plus the rest.
  SYNC_BODY = 4.megabytes

  #
  # A restore's sync (restore_deploy: its id, with its token in
  # X-Houston-Deploy-Token) only checks: its snapshot's compose.yml is valid
  # and its required secrets have values. It's kept with the restore, which
  # applies it at its switch (Deploy#report!); nothing is stored on the
  # project, placed or pointed now. Either way, serving_generation (what
  # kamal-proxy routes to, as the runner read it) catches the project up
  # after a restore that switched but never said so.
  def sync
    payload = json_body(limit: SYNC_BODY) or return
    sync = ProjectSync.new(payload)
    return render json: { error: "compose.yml doesn't match what Houston expects", errors: sync.errors }, status: :unprocessable_entity unless sync.valid?
    return check(sync, payload) if payload.key?("restore_deploy")
    return if payload.key?("claimed_deploy") && !claim_matches?(payload)

    if (existing = Project.find_by(name: payload["name"]))
      # A restore owns the project's config until it's done: its safety
      # snapshot and its cleanup read it. One gone silent doesn't: a hand
      # houston deploy (which syncs first) is how it's taken over.
      restoring = existing.deploys.where(kind: "restore").where(status: "queued")
                          .or(existing.deploys.where(kind: "restore", status: "in_flight", heartbeat_at: Deploy::STALE_AFTER.ago..)).first
      if restoring
        return render json: { error: "restore ##{restoring.number} is #{restoring.status.humanize(capitalize: false)}; wait for it" }, status: :conflict
      end
      existing.catch_up_generation!(payload["serving_generation"], payload["serving_sha"])
    end
    project = sync.save!
    dns = sync.point_dns!
    domains = sync.point_domains!
    push_maintenance_routes(project)
    if (missing = project.missing_secrets).any?
      return render json: { error: "HOLD: set #{missing.to_sentence} in Mission Control first", missing: }, status: :unprocessable_entity
    end
    # Only a sync that goes on to deploy makes volumes: until then (a HOLD,
    # say) where they live can still be chosen.
    VolumePlacement.new(project).place!
    render json: { project: project.name, host: project.host, dns:, domains:, generation: project.data_generation }
  rescue ProjectSync::Refused, VolumePlacement::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  rescue Cloudflare::Error => e
    render json: { error: "Cloudflare said no while pointing #{project&.host} at the tunnel: #{e.message}" }, status: :bad_gateway
  end

  private
    # A runner's sync names the deploy it claimed, with that deploy's token:
    # the repo's compose.yml must name the claimed deploy's project, so one
    # repo can't sync as another project.
    def claim_matches?(payload)
      claimed = Deploy.find_by(id: payload["claimed_deploy"])
      unless claimed
        render json: { error: "no such deploy" }, status: :unprocessable_entity
        return false
      end
      unless claimed.owned_by?(request.headers["X-Houston-Deploy-Token"])
        render json: { error: "that token isn't this deploy's" }, status: :forbidden
        return false
      end
      unless claimed.project.name == payload["name"]
        render json: { error: "compose.yml names project #{payload["name"].inspect}, but deploy ##{claimed.number} is for #{claimed.project.name}; nothing was synced" },
               status: :unprocessable_entity
        return false
      end
      true
    end

    def check(sync, payload)
      restore = Deploy.find_by(id: payload["restore_deploy"])
      return render json: { error: "no such restore" }, status: :unprocessable_entity unless restore&.restore? && restore.project.name == payload["name"]
      return render json: { error: "that token isn't this restore's" }, status: :forbidden unless restore.owned_by?(request.headers["X-Houston-Deploy-Token"])

      project = restore.project
      adopted = project.catch_up_generation!(payload["serving_generation"], payload["serving_sha"])
      point_best_effort(adopted) if adopted
      # Ownership and "still in flight" are checked in the write.
      kept = Deploy.where(id: restore.id, status: "in_flight").update_all(sync_payload: payload.except("restore_deploy", "serving_generation", "serving_sha"))
      return render json: { error: "restore ##{restore.number} is no longer in flight" }, status: :conflict unless kept == 1
      if (missing = sync.missing_secrets(project)).any?
        return render json: { error: "HOLD: set #{missing.to_sentence} in Mission Control first", missing: }, status: :unprocessable_entity
      end
      render json: { project: project.name, host: project.host, dns: Installation.current.dns_mode == "per_host" ? "per_host" : "wildcard",
                     domains: project.domain_states.to_h, generation: project.reload.data_generation }
    end

    def point_best_effort(sync)
      sync.point_dns!
      sync.point_domains!
    rescue ProjectSync::Refused, Cloudflare::Error => e
      Rails.logger.warn("DNS for #{sync.project.name}'s restored compose.yml wasn't pointed: #{e.message}; the next deploy points it")
    end

    # A project in maintenance keeps every hostname on the page, including
    # custom domains this sync added or dropped. Best effort: the deploy
    # goes on, and the admin's next toggle pushes again.
    def push_maintenance_routes(project)
      return unless project.maintenance?
      TunnelRoutes.push!
    rescue Cloudflare::Error => e
      Rails.logger.warn("maintenance routes for #{project.name} weren't updated: #{e.message}")
    end
end
