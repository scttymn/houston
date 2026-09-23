class Api::DeploysController < Api::BaseController
  # POST /api/projects/:name/deploys {sha, ref}
  def create
    body = json_body or return
    project = Project.find_by(name: params[:name])
    return render json: { error: "no project #{params[:name]}; houston deploy syncs it first" }, status: :not_found unless project

    deploy, token, took_over = Deploy.start!(project, sha: body["sha"], ref: body["ref"])
    render json: { id: deploy.id, number: deploy.number, token:, took_over: }, status: :created
  rescue Deploy::Busy => e
    render json: { error: e.message, number: e.deploy.number }, status: :conflict
  rescue ActiveRecord::RecordNotUnique
    render json: { error: "another deploy of #{project.name} just started" }, status: :conflict
  rescue ActiveRecord::RecordInvalid => e
    render json: { error: e.record.errors.full_messages.to_sentence }, status: :unprocessable_entity
  end

  # PATCH /api/deploys/:id with X-Houston-Deploy-Token: progress, log, result.
  # Ownership and "still in flight" are checked in the transaction that writes.
  def update
    body = json_body(limit: Deploy::CHUNK_CAP + 4.kilobytes) or return
    progress = body.slice("step", "log", "status", "error")
    return render json: { error: "a log chunk can be at most #{Deploy::CHUNK_CAP / 1.kilobyte} KiB" }, status: :content_too_large if progress["log"].is_a?(String) && progress["log"].bytesize > Deploy::CHUNK_CAP
    invalid = invalid_progress(progress)
    return render json: { error: invalid }, status: :unprocessable_entity if invalid

    status, json, deploy, appended = Deploy.transaction { apply(progress) }
    DeployBroadcast.progress(deploy, appended:, changed: progress.except("log").any?) if status == :ok
    point_adopted(deploy.adopted) if status == :ok && deploy.adopted
    render json:, status:
  end

  private
    def apply(progress)
      deploy = Deploy.find_by(id: params[:id])
      return [ :not_found, { error: "no such deploy" } ] unless deploy
      return [ :forbidden, { error: "that token isn't this deploy's" } ] unless deploy.owned_by?(request.headers["X-Houston-Deploy-Token"])
      unless deploy.in_flight?
        return [ :conflict, { error: "deploy ##{deploy.number} is no longer in flight (#{deploy.error.presence || deploy.status}); it was finished or taken over" } ]
      end

      appended = deploy.report!(**progress.symbolize_keys)
      [ :ok, { number: deploy.number, status: deploy.status }, deploy, appended ]
    rescue ActiveRecord::RecordInvalid => e
      [ :unprocessable_entity, { error: e.record.errors.full_messages.to_sentence } ]
    end

    # A restore's compose.yml, applied at its switch: its DNS, after the
    # commit. Best effort: the next deploy points it again.
    def point_adopted(sync)
      sync.point_dns!
      sync.point_domains!
    rescue ProjectSync::Refused, Cloudflare::Error => e
      Rails.logger.warn("DNS for #{sync.project.name}'s restored compose.yml wasn't pointed: #{e.message}; the next deploy points it")
    end

    def invalid_progress(progress)
      return "status must be go or no_go" if progress.key?("status") && !progress["status"].in?(%w[go no_go])
      return "step must be at most 100 characters" if progress.key?("step") && !(progress["step"].is_a?(String) && progress["step"].length <= 100)
      return "error must be at most 1000 characters" if progress.key?("error") && !(progress["error"].is_a?(String) && progress["error"].length <= 1000)
      "log must be text" if progress.key?("log") && !progress["log"].is_a?(String)
    end
end
