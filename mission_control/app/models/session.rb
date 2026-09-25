# A signed-in browser. It ends after IDLE unused, or LIFETIME after signing
# in, and works only the way it was made: through the tunnel, or not
# (a cookie from plain HTTP on the network can't be replayed at admin.<base>).
class Session < ApplicationRecord
  IDLE = 2.weeks
  LIFETIME = 30.days
  # How often use is written down.
  SEEN_EVERY = 1.hour

  belongs_to :user

  # The session a cookie names, if it works for this request: made the same
  # way (tunnel: the request came through Cloudflare, which adds Cf-Ray),
  # and not ended. An ended one is deleted. Pages and the live updates both
  # come through here.
  def self.resume(id, tunnel:)
    session = find_by(id:) if id
    return unless session && session.tunnel == tunnel
    return session.destroy && nil if session.ended?
    session.tap(&:used!)
  end

  def ended?
    created_at < LIFETIME.ago || (last_active_at || created_at) < IDLE.ago
  end

  def used!
    update_column(:last_active_at, Time.current) if last_active_at.nil? || last_active_at < SEEN_EVERY.ago
  end
end
