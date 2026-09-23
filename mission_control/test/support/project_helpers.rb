# Projects and deploys as houston deploy leaves them.
module ProjectHelpers
  def make_project(name, services: %w[app], domains: [], variables: [], deploy_rule: { "on" => "commit", "branch" => "main" })
    # Like a sync: the project claims its container names.
    Project.create!(name:, app_service: "app", services:, domains:, variables:, health: "/up", port: 80, deploy_rule:).tap do |project|
      project.host_names.each { |host| project.hosts.create!(name: host) }
    end
  end

  def make_deploy(project, number, status, sha: format("%040x", number), step: nil, log: "", error: nil, started: number.hours.ago)
    project.deploys.create!(number:, sha:, ref: "refs/heads/main", status:, step:, log:, error:, token_digest: "d",
                            heartbeat_at: started, created_at: started,
                            finished_at: (status == "in_flight" ? nil : started + 90.seconds))
  end

  WEBHOOK_SECRET = "whsec-#{"x" * 40}"

  # A project Add project linked: a repo, a deploy key, a webhook secret.
  def make_linked_project(name, deploy_rule: { "on" => "commit", "branch" => "main" }, **options)
    make_project(name, deploy_rule:, **options).tap do |project|
      project.update!(repo_url: "git@forgejo:houston/#{name}.git", branch: "main", compose_path: "compose.yml",
                      deploy_key_private: "-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----\n",
                      deploy_key_public: "ssh-ed25519 AAAAfake houston@svnmns.com", webhook_secret: WEBHOOK_SECRET)
    end
  end
end
