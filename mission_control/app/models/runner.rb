# A houston-runner-N container, as seen when it polls for work.
class Runner < ApplicationRecord
  NAME = /\Ahouston-runner-\d+\z/

  def self.seen!(name)
    upsert({ name:, last_seen_at: Time.current }, unique_by: :name)
  end

  def self.live_count = where(last_seen_at: 60.seconds.ago..).count
end
