# POST hooks.<base>/<name>: the doorbell. A verified push only makes Houston
# look at the repo's refs itself (CheckForChangesJob); the payload is never
# read. Anything unverified gets the same empty 404 as an unknown project.
class WebhooksController < ActionController::API
  MAX_BODY = 5.megabytes

  # The name comes from the path; params would parse the body, which is never read as JSON.
  wrap_parameters false
  rate_limit to: 30, within: 1.minute, by: -> { request.path_parameters[:name] }, with: -> { head :too_many_requests }, only: :create

  def create
    body = request.body.read(MAX_BODY + 1).to_s
    return head :content_too_large if body.bytesize > MAX_BODY

    project = Project.find_by(name: request.path_parameters[:name])
    return head :not_found unless project && Webhook.verified?(request.headers, body, project.webhook_secret)

    project.update_column(:webhook_verified_at, Time.current) unless project.webhook_verified_at
    CheckForChangesJob.perform_later(project.id)
    head :accepted
  end

  def not_found
    head :not_found
  end
end
