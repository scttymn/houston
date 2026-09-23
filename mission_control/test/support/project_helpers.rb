# Projects and deploys as houston deploy leaves them.
module ProjectHelpers
  def make_project(name, services: %w[app], domains: [], variables: [], deploy_rule: { "on" => "commit", "branch" => "main" })
    Project.create!(name:, app_service: "app", services:, domains:, variables:, health: "/up", port: 80, deploy_rule:)
  end

  def make_deploy(project, number, status, sha: format("%040x", number), step: nil, log: "", error: nil, started: number.hours.ago)
    project.deploys.create!(number:, sha:, ref: "refs/heads/main", status:, step:, log:, error:, token_digest: "d",
                            heartbeat_at: started, created_at: started,
                            finished_at: (status == "in_flight" ? nil : started + 90.seconds))
  end
end
