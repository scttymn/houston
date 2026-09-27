# A project's repo moved or renamed on its git host (a copy keeps the old
# project's URL; GitHub forwards a renamed repo's old URL only until the name
# is reused). The URL changes once Houston can read the new one with the
# project's own deploy key and its compose.yml names the project; the key,
# the webhook secret and the refs already seen stay, so nothing redeploys.
class RepoMove
  class Refused < StandardError; end

  # What GitRemote reads a repo with: the project's, at the new URL.
  Candidate = Struct.new(:repo_url, :branch, :compose_path, :deploy_key_private, keyword_init: true)

  def initialize(project, repo_url)
    @project = project
    @repo_url = repo_url.to_s.strip
  end

  def call!
    unless @repo_url.match?(RepoLink::URL)
      raise Refused, "the repo URL must be an ssh://, https:// or user@host:path repo URL (no credentials, options or local paths)"
    end
    raise Refused, "#{@project.name}'s repo is already #{@repo_url}" if @repo_url == @project.repo_url
    if (busy = @project.deploys.where(status: %w[queued in_flight]).order(:number).first)
      raise Refused, "#{busy.kind == "deploy" ? "deploy" : busy.kind} ##{busy.number} is #{busy.status.humanize(capitalize: false)}; wait for ##{busy.number}"
    end

    candidate = Candidate.new(repo_url: @repo_url, branch: @project.branch, compose_path: @project.compose_path, deploy_key_private: @project.deploy_key_private)
    access = GitRemote.check(candidate)
    raise Refused, "#{@repo_url}: #{access.message}" unless access.ok
    read = GitRemote.read(candidate)
    raise Refused, "couldn't read #{@repo_url}: #{read.problems}" unless read.ok
    named = read.inspection.dig("sync", "name")
    raise Refused, "#{@repo_url}'s compose.yml there names #{named}, not #{@project.name}" unless named == @project.name

    @project.update!(repo_url: @repo_url, last_check_error: nil)
  end
end
