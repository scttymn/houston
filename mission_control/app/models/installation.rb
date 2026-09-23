# This server's Houston setup: one row. Cloudflare tokens are encrypted at
# rest, so a copy of the database in a backup doesn't expose them.
class Installation < ApplicationRecord
  encrypts :cloudflare_api_token, :tunnel_token

  def self.current
    first || new
  end

  def self.connected?
    where.not(cloudflare_connected_at: nil).exists?
  end

  def connected?
    cloudflare_connected_at.present?
  end
end
