require "open3"

# A repo being linked on the Add project page: its own deploy key from the
# start (so the admin can add it to the repo before Houston checks access),
# then the branch and compose path, then what `houston inspect` read. Save
# turns it into the project's link (ProjectLinking).
class RepoLink < ApplicationRecord
  encrypts :deploy_key_private

  # Only what git should ever be asked to fetch: https://, ssh://, or
  # scp-style user@host:path. No credentials in the URL, no options (-x),
  # no local paths or file://, no ext:: transports.
  HOST = /[A-Za-z0-9][A-Za-z0-9.-]*/
  PATH = %r{[A-Za-z0-9._~/-]+}
  URL = %r{\A(?:https://#{HOST}(?::\d+)?/#{PATH}|ssh://(?:[A-Za-z0-9._-]+@)?#{HOST}(?::\d+)?/#{PATH}|[A-Za-z0-9._-]+@#{HOST}:(?![-/])#{PATH})\z}
  # git check-ref-format --branch, closely enough to refuse anything unsafe.
  BRANCH = %r{\A(?![-/.])(?!.*\.\.)(?!.*//)(?!.*@\{)[^\x00-\x20~^:?*\[\\\x7f]+(?<![./])(?<!\.lock)\z}
  COMPOSE_PATH = %r{\A(?![-/])(?!(?:.*/)?\.\.(?:/|\z))[A-Za-z0-9._/-]+\.ya?ml\z}

  validates :repo_url, format: { with: URL, message: "must be an ssh://, https:// or user@host:path repo URL (no credentials, options or local paths)" }
  validates :branch, format: { with: BRANCH, message: "isn't a branch name git accepts" }
  validates :compose_path, format: { with: COMPOSE_PATH, message: "must be a .yml or .yaml path inside the repo" }

  # A new draft for repo_url with a fresh ed25519 key. Drafts older than a
  # day are dropped: nobody finishes linking a repo a day later.
  def self.start!(repo_url)
    where(created_at: ...1.day.ago).delete_all
    private_key, public_key = generate_key
    create!(repo_url:, deploy_key_private: private_key, deploy_key_public: public_key)
  end

  def self.generate_key
    Dir.mktmpdir("houston-key") do |dir|
      path = File.join(dir, "key")
      comment = "houston@#{Installation.current.base_domain.presence || "houston"}"
      _, status = Open3.capture2e("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", path)
      raise "ssh-keygen failed" unless status.success?
      [ File.read(path), File.read("#{path}.pub").strip ]
    end
  end

  def found? = preview.present?
end
