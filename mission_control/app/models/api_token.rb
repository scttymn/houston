# A named personal token for the remote API (/api/v1): the houston CLI
# with --server, or an agent. Only its SHA-256 is stored; the token itself
# is shown once, when it's issued.
class ApiToken < ApplicationRecord
  PREFIX = "hou_"

  validates :name, presence: true, length: { maximum: 50 }, uniqueness: true

  def self.digest(token) = OpenSSL::Digest::SHA256.hexdigest(token)

  # Returns [token, record]; raises ActiveRecord::RecordInvalid for a bad name.
  def self.issue!(name)
    token = PREFIX + SecureRandom.urlsafe_base64(32)
    [ token, create!(name:, token_digest: digest(token)) ]
  end

  def self.authenticate(token)
    return nil unless token.to_s.start_with?(PREFIX)
    find_by(token_digest: digest(token))
  end

  # At most one write a minute, however busy the token is.
  def used!
    update_column(:last_used_at, Time.current) if last_used_at.nil? || last_used_at < 1.minute.ago
  end
end
