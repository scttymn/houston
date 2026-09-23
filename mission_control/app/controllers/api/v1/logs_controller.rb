# GET /api/v1/projects/:name/logs: the running app container's output,
# streamed (houston logs --server [-f]).
class Api::V1::LogsController < Api::V1::BaseController
  include ActionController::Live

  MAX_TAIL = 10_000
  MAX_FOLLOW = 1.hour

  def show
    project = Project.find_by(name: params[:project_name])
    return render(json: { error: "no project #{params[:project_name]}" }, status: :not_found) unless project

    tail = (params[:tail].presence || "200")
    unless tail.match?(/\A\d+\z/) && tail.to_i.between?(1, MAX_TAIL)
      return render json: { error: "tail must be 1–#{MAX_TAIL}" }, status: :unprocessable_entity
    end

    container = DockerCommand.run("ps", "--filter", "label=service=#{project.name}", "--filter", "label=role=web", "--format", "{{.Names}}").output.lines.first.to_s.strip
    return render json: { error: "#{project.name} isn't running" }, status: :not_found if container.empty?

    follow = params[:follow] == "1"
    args = [ "logs", "--timestamps", "--tail", tail ]
    args << "--follow" if follow
    args << container
    response.headers["Content-Type"] = "text/plain; charset=utf-8"
    DockerCommand.stream(*args, timeout: (MAX_FOLLOW.to_i if follow)) { |chunk| response.stream.write(chunk) }
  rescue ActionController::Live::ClientDisconnected, IOError
    # The client went away; stream's ensure has stopped docker.
  ensure
    response.stream.close
  end
end
