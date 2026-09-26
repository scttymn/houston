# Records how a server update went, once its helper has ended, and what a
# running one is doing (ServerUpdate.settle!). The schedule runs it every
# minute (config/recurring.yml); while an update runs, it also follows it
# every FOLLOW_EVERY, starting from ServerUpdate.start!, so the board can
# show each step. Queued jobs outlive Mission Control's restart.
class ServerUpdateJob < ApplicationJob
  FOLLOW_EVERY = 5.seconds

  queue_as :default

  def perform(follow = false)
    ServerUpdate.settle!
    self.class.set(wait: FOLLOW_EVERY).perform_later(true) if follow && ServerUpdate.running?
  end
end
