# Save on the Add project page: the project from what houston inspect read
# (through ProjectSync, so its rules and the container-name index apply),
# linked to the repo. The same model is what `houston link --server` will
# call (build step 4b).
class ProjectLinking
  class Refused < StandardError; end

  def initialize(link)
    @link = link
  end

  def save!
    raise Refused, "Read the file first: Houston saves what it read from the repo." unless @link&.found?

    sync = ProjectSync.new(@link.preview["sync"])
    raise Refused, sync.errors.map { |field, messages| "#{field} #{messages.to_sentence}" }.to_sentence unless sync.valid?

    existing = Project.find_by(name: @link.preview.dig("sync", "name"))
    if existing&.repo_url.present? && existing.repo_url != @link.repo_url
      raise Refused, "#{existing.name} is already linked to #{existing.repo_url}"
    end

    project = sync.save!(link: {
      repo_url: @link.repo_url, branch: @link.branch, compose_path: @link.compose_path,
      deploy_key_private: @link.deploy_key_private, deploy_key_public: @link.deploy_key_public,
      webhook_secret: existing&.webhook_secret.presence || SecureRandom.urlsafe_base64(32)
    })
    @link.destroy!
    project
  rescue ProjectSync::Refused => e
    raise Refused, e.message
  end
end
