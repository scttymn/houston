# The flight board's resources column, read now (AppStats.fresh!): an open
# board asks every 30 seconds while it's in view (stats_controller.js), and
# gets each project's gauges as Turbo Stream updates, for its row and its card.
class ResourcesController < ApplicationController
  def index
    AppStats.fresh!
    streams = Project.order(:name).map do |project|
      turbo_stream.update_all("[data-resources='#{project.name}']", partial: "projects/resources", locals: { project: })
    end
    render turbo_stream: streams
  end
end
