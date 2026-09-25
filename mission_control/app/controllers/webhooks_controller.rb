# POST hooks.<base>/<name>: the doorbell. A verified push only makes Houston
# look at the repo's refs itself (CheckForChangesJob); the payload is never
# read. Anything unverified gets the same empty 404 as an unknown project.
#
# Limits, a minute at a time (security fixes, M4): requests that don't
# verify are counted per client address and path, and checked before the
# body is read, so junk can't block real pushes from another address, nor
# (sent through GitHub, from GitHub's addresses) to another project.
# Verified ones are counted per project. PollForChangesJob catches a push
# whose ring was lost anyway.
class WebhooksController < ActionController::API
  MAX_BODY = 5.megabytes
  UNVERIFIED = 30 # per address and path
  VERIFIED = 60 # per project

  # The name comes from the path; params would parse the body, which is never read as JSON.
  wrap_parameters false

  def create
    return head :too_many_requests if count("unverified", sender) >= UNVERIFIED

    body = request.body.read(MAX_BODY + 1).to_s
    return unverified(:content_too_large) if body.bytesize > MAX_BODY

    project = Project.find_by(name: request.path_parameters[:name])
    return unverified(:not_found) unless project && Webhook.verified?(request.headers, body, project.webhook_secret)
    return head :too_many_requests if count!("verified", project.name) > VERIFIED

    project.update_column(:webhook_verified_at, Time.current) unless project.webhook_verified_at
    CheckForChangesJob.perform_later(project.id)
    head :accepted
  end

  def not_found
    head :not_found
  end

  private
    def unverified(status)
      count!("unverified", sender)
      head status
    end

    def sender = "#{request.remote_ip}:#{request.path_parameters[:name]}"
    def key(kind, who) = "webhooks:#{kind}:#{who}:#{Time.current.to_i / 60}"
    def count(kind, who) = Rails.cache.read(key(kind, who), raw: true).to_i
    def count!(kind, who) = Rails.cache.increment(key(kind, who), 1, expires_in: 2.minutes).to_i
end
