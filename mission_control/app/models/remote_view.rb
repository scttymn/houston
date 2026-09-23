# What the remote API (/api/v1) shows of projects and deploys. The one place
# those are serialized for personal tokens: no secret values, no deploy key,
# no webhook secret, ever.
module RemoteView
  LOG_CHUNK = 256.kilobytes

  def self.project(project, detail: false)
    last = project.latest_deploy
    view = {
      name: project.name, status: project.status.to_s, running_sha: project.running_deploy&.sha,
      host: project.host, domains: project.domain_states, last_deploy: last && deploy(last)
    }
    return view unless detail

    have = project.secrets.select { |s| s.value.present? }.map(&:key)
    view.merge(
      deploy_rule: project.deploy_rule, services: project.services, repo_url: project.repo_url, branch: project.branch,
      webhook_verified: project.webhook_verified_at.present?,
      secrets: project.variables.map { |v| { name: v["name"], required: v["required"] == true, set: have.include?(v["name"]) } }
    )
  end

  def self.deploy(deploy)
    { number: deploy.number, status: deploy.status, sha: deploy.sha, ref: deploy.ref, step: deploy.step, error: deploy.error,
      runner: deploy.runner, started_at: deploy.created_at, finished_at: deploy.finished_at, duration: deploy.duration }
  end

  # The deploy with its steps and up to LOG_CHUNK of its log from byte from,
  # on whole UTF-8 characters; log_next is where to ask from next.
  def self.deploy_with_log(deploy, from)
    log = deploy.log.to_s.b
    start = [ from.to_i, 0 ].max
    start += 1 while start < log.bytesize && (log.getbyte(start) & 0xC0) == 0x80
    chunk = log.byteslice(start, LOG_CHUNK) || "".b
    chunk = chunk.byteslice(0, chunk.bytesize - 1) until chunk.dup.force_encoding("UTF-8").valid_encoding?
    deploy(deploy).merge(
      steps: deploy.step_states.map { |name, state| { name:, state: state.to_s } },
      log: chunk.force_encoding("UTF-8"), log_size: log.bytesize, log_next: start + chunk.bytesize
    )
  end
end
