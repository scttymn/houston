class Api::ProjectsController < Api::BaseController
  # POST /api/projects/sync. Saves the project first, so the admin can fill in
  # missing secrets even when the answer is HOLD; DNS is ensured on every
  # sync, so a retry after a Cloudflare failure finishes the job.
  def sync
    payload = json_body or return
    sync = ProjectSync.new(payload)
    return render json: { error: "compose.yml doesn't match what Houston expects", errors: sync.errors }, status: :unprocessable_entity unless sync.valid?

    project = sync.save!
    VolumePlacement.new(project).place!
    dns = sync.point_dns!
    domains = sync.point_domains!
    if (missing = project.missing_secrets).any?
      return render json: { error: "HOLD: set #{missing.to_sentence} in Mission Control first", missing: }, status: :unprocessable_entity
    end
    render json: { project: project.name, host: project.host, dns:, domains: }
  rescue ProjectSync::Refused, VolumePlacement::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  rescue Cloudflare::Error => e
    render json: { error: "Cloudflare said no while pointing #{project&.host} at the tunnel: #{e.message}" }, status: :bad_gateway
  end
end
