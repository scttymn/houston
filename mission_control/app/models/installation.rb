# This server's Houston setup: one row. Cloudflare tokens are encrypted at
# rest, so a copy of the database in a backup doesn't expose them.
class Installation < ApplicationRecord
  encrypts :cloudflare_api_token, :tunnel_token

  def self.current
    first || new
  end

  # Names this install on /ping, so Mission Control can tell its own answer
  # from another server's. Derived one-way from secret_key_base.
  def self.identity
    Rails.application.key_generator.generate_key("houston/identity", 16).unpack1("H*")
  end

  def self.connected?
    where.not(cloudflare_connected_at: nil).exists?
  end

  def connected?
    cloudflare_connected_at.present?
  end
end
