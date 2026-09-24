# Live updates for a deploy's page (Turbo Streams over Solid Cable): the
# log as it's appended, and the status and steps when they change. Sent
# after the report's transaction commits.
module DeployBroadcast
  def self.progress(deploy, appended: nil, changed: true)
    Time.use_zone(Installation.current.zone) { broadcast(deploy, appended:, changed:) }
  end

  # Rendered in the Settings zone, as the page itself is (ApplicationController).
  def self.broadcast(deploy, appended:, changed:)
    if appended.present?
      Turbo::StreamsChannel.broadcast_append_to(deploy, target: "deploy_log", html: ERB::Util.html_escape(appended))
    end
    return unless changed

    project = deploy.project
    Turbo::StreamsChannel.broadcast_replace_to(deploy, target: "deploy_status", partial: "deploys/status",
                                               locals: { deploy:, project:, running: project.running_deploy })
    Turbo::StreamsChannel.broadcast_replace_to(deploy, target: "deploy_steps", partial: "deploys/steps", locals: { deploy: })
  end
end
