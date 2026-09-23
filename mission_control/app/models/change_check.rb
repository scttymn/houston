# Check for changes: read the repo's refs with its deploy key, and queue a
# deploy for each ref the deploy rule matches that moved since the last
# check. What was seen is saved in the same transaction as the queueing.
class ChangeCheck
  def initialize(project)
    @project = project
  end

  # Returns the deploys it queued.
  def run
    result = GitRemote.refs(@project)
    unless result.ok
      @project.update!(last_check_error: result.error, last_checked_at: Time.current)
      return []
    end

    wanted = matching(result.refs)
    Project.transaction do
      queued = wanted.reject { |ref, sha| @project.seen_refs[ref] == sha }.sort.map { |ref, sha| Deploy.queue!(@project, sha:, ref:) }
      @project.update!(seen_refs: wanted, last_checked_at: Time.current, last_check_error: nil)
      queued
    end
  end

  private
    # ref → commit, for the refs the rule deploys. An annotated tag's peeled
    # ref (^{}) names the commit it points at.
    def matching(refs)
      rule = @project.deploy_rule
      if rule["on"] == "tag"
        glob = rule["tags"].presence || "v*"
        refs.keys.grep(%r{\Arefs/tags/[^^]+\z}).select { |ref| File.fnmatch(glob, ref.delete_prefix("refs/tags/")) }
            .to_h { |ref| [ ref, refs["#{ref}^{}"] || refs[ref] ] }
      else
        ref = "refs/heads/#{rule["branch"].presence || "main"}"
        refs.key?(ref) ? { ref => refs[ref] } : {}
      end
    end
end
