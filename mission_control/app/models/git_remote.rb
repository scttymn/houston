require "open3"

# Runs git against a linked repo with its deploy key, and `houston inspect`
# on what it fetched. The key goes into a 0600 temp file for one command and
# is removed with it. ssh trusts a git host's key the first time it sees it
# (storage/known_hosts, on the persistent volume) and refuses a changed one.
# Tests swap the runner for a recording fake.
class GitRemote
  Result = Data.define(:success, :output)
  Access = Data.define(:ok, :message)
  Read = Data.define(:ok, :sha, :inspection, :problems)
  Refs = Data.define(:ok, :refs, :error)

  class Runner
    def call(args, env)
      output, status = Open3.capture2e(env, "timeout", "60", *args)
      Result.new(success: status.success?, output:)
    rescue SystemCallError => e
      Result.new(success: false, output: e.message)
    end
  end

  class_attribute :runner, default: Runner.new

  def self.known_hosts = ENV.fetch("HOUSTON_KNOWN_HOSTS", Rails.root.join("storage", "known_hosts").to_s)
  def self.houston_bin = ENV.fetch("HOUSTON_BIN", "houston")

  # The host key lines recorded for a repo's SSH host (runners trust only
  # these; they never accept a new key themselves). [] for HTTPS or an
  # unseen host.
  def self.known_hosts_for(repo_url)
    spec = case repo_url
    when %r{\Assh://(?:[^@/]+@)?([^:/]+):(\d+)/} then "[#{$1}]:#{$2}"
    when %r{\Assh://(?:[^@/]+@)?([^:/]+)/} then $1
    when %r{\A[^@/]+@([^:/]+):} then $1
    end
    return [] unless spec && File.exist?(known_hosts)

    output, status = Open3.capture2e("ssh-keygen", "-F", spec, "-f", known_hosts)
    status.success? ? output.lines.map(&:strip).reject { |l| l.empty? || l.start_with?("#") } : []
  end

  # Can Houston read the repo, and does it have the branch?
  def self.check(link)
    with_key(link) do |env, key|
      result = runner.call([ "git", "ls-remote", "--heads", "--", link.repo_url ], env)
      next Access.new(ok: false, message: explain(result.output, key)) unless result.success

      if result.output.lines.any? { |line| line.split("\t").last&.strip == "refs/heads/#{link.branch}" }
        Access.new(ok: true, message: "Houston can read the repo")
      else
        Access.new(ok: false, message: "Houston can read the repo, but it has no branch #{link.branch}")
      end
    end
  end

  # Every branch and tag, ref → sha (with peeled ^{} entries for tags).
  def self.refs(project)
    with_key(project) do |env, key|
      result = runner.call([ "git", "ls-remote", "--heads", "--tags", "--", project.repo_url ], env)
      next Refs.new(ok: false, refs: {}, error: explain(result.output, key)) unless result.success

      refs = result.output.lines.filter_map { |line| sha, ref = line.strip.split("\t", 2); [ ref, sha ] if ref && sha.to_s.match?(/\A\h{40}\z/) }.to_h
      Refs.new(ok: true, refs:, error: nil)
    end
  end

  # Fetches only the compose file at the branch's head and reads it with
  # houston inspect, so Mission Control never interprets compose.yml itself.
  def self.read(link)
    with_key(link) do |env, key, dir|
      checkout = File.join(dir, "repo")
      steps = [
        [ "git", "clone", "--depth", "1", "--single-branch", "--branch", link.branch, "--no-tags", "--filter=blob:none", "--no-checkout", "--", link.repo_url, checkout ],
        [ "git", "-C", checkout, "checkout", "HEAD", "--", link.compose_path ]
      ]
      failed = steps.lazy.map { |args| runner.call(args, env) }.find { |r| !r.success }
      next Read.new(ok: false, sha: nil, inspection: nil, problems: explain(failed.output, key)) if failed

      sha = runner.call([ "git", "-C", checkout, "rev-parse", "HEAD" ], env).output.strip
      inspected = runner.call([ houston_bin, "-f", File.join(checkout, link.compose_path), "inspect", "--json" ], {})
      next Read.new(ok: false, sha:, inspection: nil, problems: inspected.output.gsub(checkout + "/", "")) unless inspected.success

      Read.new(ok: true, sha:, inspection: JSON.parse(inspected.output), problems: nil)
    end
  rescue JSON::ParserError
    Read.new(ok: false, sha: nil, inspection: nil, problems: "houston inspect didn't answer with JSON")
  end

  def self.with_key(link)
    Dir.mktmpdir("houston-git") do |dir|
      key = File.join(dir, "deploy_key")
      File.open(key, "w", 0o600) { |f| f.write(link.deploy_key_private.end_with?("\n") ? link.deploy_key_private : "#{link.deploy_key_private}\n") }
      env = {
        "GIT_SSH_COMMAND" => "ssh -i #{key} -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=10 " \
                             "-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=#{known_hosts}",
        "GIT_TERMINAL_PROMPT" => "0",
        "GIT_ALLOW_PROTOCOL" => "ssh:https"
      }
      yield env, key, dir
    end
  end

  # git's answer in one line, with what to do about the usual ones.
  def self.explain(output, key)
    text = output.to_s.gsub(key, "<deploy key>")
    if text.include?("REMOTE HOST IDENTIFICATION HAS CHANGED")
      return "the git host's host key changed since Houston first saw it. If that's expected, remove its line from #{known_hosts} on the server, then check again."
    end
    line = text.lines.map(&:strip).find { |l| l.match?(/denied|fatal|error|could not|not found|timed out/i) } || text.lines.first.to_s.strip
    line += ". Add the deploy key above to the repo as a read-only deploy key, then check again." if line.include?("Permission denied")
    line.presence || "git failed without saying why"
  end
end
