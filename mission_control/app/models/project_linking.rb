# Save on the Add project page: the project from what houston inspect read
# (through ProjectSync, so its rules and the container-name index apply),
# linked to the repo, with the secrets typed on the page. The same model is
# what `houston link --server` calls.
class ProjectLinking
  class Refused < StandardError; end

  # A secret the container can't receive: nothing is saved.
  class SecretsRefused < Refused
    attr_reader :errors # name → message

    def initialize(errors)
      @errors = errors
      super("#{errors.keys.to_sentence} can't be saved")
    end
  end

  # secrets: name → value, from Add project; blank ones and names compose.yml
  # doesn't list are left out.
  def initialize(link, secrets: {})
    @link = link
    @secrets = secrets.to_h.transform_keys(&:to_s)
  end

  # The project's own when it's linked again (a repo's webhook keeps working),
  # else the draft's: what Add project shows in step 04 before Save.
  def webhook_secret
    existing&.webhook_secret.presence || @link.webhook_secret.presence
  end

  # Once a project's pushes arrive, Add project doesn't show its secret again.
  def pushes_arrive? = existing&.webhook_verified_at.present?

  def save!
    raise Refused, "Read the file first: Houston saves what it read from the repo." unless @link&.found?

    sync = ProjectSync.new(@link.preview["sync"])
    raise Refused, sync.errors.map { |field, messages| "#{field} #{messages.to_sentence}" }.to_sentence unless sync.valid?

    if existing&.repo_url.present? && existing.repo_url != @link.repo_url
      raise Refused, "#{existing.name} is already linked to #{existing.repo_url}"
    end

    secrets = wanted_secrets
    errors = secrets.to_h { |key, value| [ key, Secret.new(key:, value:).tap(&:validate).errors[:value].to_sentence ] }.compact_blank
    raise SecretsRefused, errors if errors.any?

    Project.transaction do
      project = sync.save!(link: {
        repo_url: @link.repo_url, branch: @link.branch, compose_path: @link.compose_path,
        deploy_key_private: @link.deploy_key_private, deploy_key_public: @link.deploy_key_public,
        webhook_secret: webhook_secret || SecureRandom.urlsafe_base64(32)
      })
      secrets.each { |key, value| project.secrets.find_or_initialize_by(key:).update!(value:) }
      @link.destroy!
      project
    end
  rescue ProjectSync::Refused => e
    raise Refused, e.message
  end

  private
    def existing
      return @existing if defined?(@existing)
      @existing = @link&.found? ? Project.find_by(name: @link.preview.dig("sync", "name")) : nil
    end

    def wanted_secrets
      names = @link.preview.dig("sync", "variables").to_a.map { |v| v["name"] }
      @secrets.slice(*names).reject { |_, value| value.blank? }
    end
end
