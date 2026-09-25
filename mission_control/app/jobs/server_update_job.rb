# Records how a server update went, once its helper has ended
# (config/recurring.yml; ServerUpdate.settle!).
class ServerUpdateJob < ApplicationJob
  queue_as :default

  def perform = ServerUpdate.settle!
end
