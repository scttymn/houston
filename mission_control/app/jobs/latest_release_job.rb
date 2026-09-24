# Checks GitHub for the latest Houston release (config/recurring.yml).
class LatestReleaseJob < ApplicationJob
  queue_as :default

  def perform = LatestRelease.check!
end
