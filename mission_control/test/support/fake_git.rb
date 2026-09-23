# Records git (and houston inspect) invocations instead of running them. A
# responder block returns a GitRemote::Result per call (default: success, no
# output).
class FakeGit
  Call = Data.define(:args, :env)

  attr_reader :calls

  def initialize(&responder)
    @calls = []
    @responder = responder
  end

  def call(args, env)
    @calls << Call.new(args:, env:)
    @responder&.call(args, env) || GitRemote::Result.new(success: true, output: "")
  end
end

module FakeGitHelper
  def use_fake_git(fake)
    original = GitRemote.runner
    GitRemote.runner = fake
    yield fake
  ensure
    GitRemote.runner = original
  end

  def git_ok(output = "") = GitRemote::Result.new(success: true, output:)
  def git_failure(output) = GitRemote::Result.new(success: false, output:)

  # What houston inspect --json prints for a small Rails + Postgres app.
  def garage_inspection
    {
      "sync" => { "name" => "garage", "app_service" => "app", "services" => %w[app db], "domains" => [ "rideclubgarage.com" ],
                  "variables" => [ { "name" => "POSTGRES_PASSWORD", "required" => true }, { "name" => "SENTRY_DSN", "required" => false } ],
                  "health" => "/up", "port" => 3000, "deploy_rule" => { "on" => "commit", "branch" => "main", "tags" => "v*" },
                  "volumes" => [ { "name" => "storage", "path" => "/rails/storage" } ] },
      "preview" => { "services" => [ { "name" => "app", "image" => "", "app" => true }, { "name" => "db", "image" => "postgres:17", "app" => false } ],
                     "port" => 3000, "health" => "/up", "cpus" => "1", "memory" => "1 GB", "test" => true,
                     "backups" => { "schedule" => "daily 03:00", "keep_auto" => 14, "keep_deploy" => 10, "volumes" => %w[pgdata storage] } }
    }
  end
end
