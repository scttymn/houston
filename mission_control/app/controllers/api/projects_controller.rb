class Api::ProjectsController < Api::BaseController
  # POST /api/projects/sync. Saves the project first, so the admin can fill in
  # missing secrets even when the answer is HOLD; DNS is ensured on every
  # sync, so a retry after a Cloudflare failure finishes the job.
  # Room for a 512 KB maintenance page after JSON escapes its < > & to six
  # bytes each (about 3 MB at worst), plus the rest.
  SYNC_BODY = 4.megabytes

  def sync
    payload = json_body(limit: SYNC_BODY) or return
    sync = ProjectSync.new(payload)
    return render json: { error: "compose.yml doesn't match what Houston expects", errors: sync.errors }, status: :unprocessable_entity unless sync.valid?

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
