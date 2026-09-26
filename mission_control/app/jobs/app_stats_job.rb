# Samples every app's CPU, memory and disk for the flight board
# (config/recurring.yml; AppStats).
class AppStatsJob < ApplicationJob
  queue_as :default

  def perform = AppStats.sample!
end
