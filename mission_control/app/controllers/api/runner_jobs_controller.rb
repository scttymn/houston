class Api::RunnerJobsController < Api::BaseController
  MAX_WAIT = 25

  # POST /api/runner/jobs/claim {runner, wait}: a long poll for the next
  # queued deploy. 200 with what the runner needs to fetch, test and deploy
  # it; 204 when there was nothing within wait seconds.
  def claim
    body = json_body or return
    runner, wait = body["runner"], body.fetch("wait", MAX_WAIT)
    unless runner.is_a?(String) && runner.match?(Runner::NAME) && wait.is_a?(Integer) && wait.between?(0, MAX_WAIT)
      return render json: { error: "runner must be houston-runner-N and wait 0–#{MAX_WAIT} seconds" }, status: :unprocessable_entity
    end

    Runner.seen!(runner)
    deadline = wait.seconds.from_now
    loop do
      if (claimed = Deploy.claim_next!(runner:))
        DeployBroadcast.progress(claimed.first)
        return render json: job(*claimed)
      end
      break if Time.current >= deadline
      sleep 1
    end
    head :no_content
  end

  private
    # For a restore, the serving generation as the config that serves has it
    # (a restore's check sync stores nothing on the project): its accessory
    # containers, and what of it a snapshot holds (app volumes, Postgres), all
    # the runner removes after the switch.
    def previous(deploy)
      return { previous_accessories: nil, previous_volumes: nil, previous_databases: nil } unless deploy.restore?
      project = deploy.project
      serving = Generation.new(project, deploy.previous_generation)
      { previous_accessories: project.accessories.sort.map { |service| serving.container(service) },
        previous_volumes: project.volumes.map { |v| serving.volume(v["name"]) },
        previous_databases: project.databases.map { |d| serving.container(d["service"]) } }
    end

    def job(deploy, token, took_over)
      project = deploy.project
      {
        deploy: { id: deploy.id, number: deploy.number, token:, sha: deploy.sha, ref: deploy.ref, took_over:,
                  kind: deploy.kind, generation: deploy.generation, previous_generation: deploy.previous_generation,
                  **previous(deploy) },
        project: { name: project.name, repo_url: project.repo_url, branch: project.branch, compose_path: project.compose_path,
                   deploy_key: project.deploy_key_private },
        known_hosts: GitRemote.known_hosts_for(project.repo_url)
      }
    end
end
