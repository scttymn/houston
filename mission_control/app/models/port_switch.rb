# Settings › Port 3000 (security fixes, H3): open to the network (for
# first-run setup, or on purpose) or closed, bound to 127.0.0.1. The choice
# is saved, and the installer reads it on every run. It's applied by having
# Mission Control recreated from its own compose.yml, which binds
# "${HOUSTON_BIND:-127.0.0.1}:3000:80": a one-off container from the runner
# image (it has docker compose) waits a moment, then runs compose with
# HOUSTON_BIND set, which recreates this container.
module PortSwitch
  class Refused < StandardError; end

  HELPER = "houston-port-3000"
  CONFIG_FILES = "com.docker.compose.project.config_files"
  WORKING_DIR = "com.docker.compose.project.working_dir"

  # Saves the choice and starts the helper. Refused, with nothing saved,
  # when it can't be applied.
  def self.set!(open:)
    labels = own_labels
    config, dir = labels[CONFIG_FILES].to_s.split(",").first, labels[WORKING_DIR]
    raise Refused, "can't change it from here: this Mission Control wasn't started by Houston's installer" if config.blank? || dir.blank?
    image = ENV["HOUSTON_RUNNER_IMAGE"].presence or
      raise Refused, "can't change it from here yet: run the installer once more (it tells Mission Control the runner image to do it with)"

    result = DockerCommand.run("run", "-d", "--rm", "--name", HELPER, "--user", "0", "-e", "HOUSTON_BIND=#{open ? "0.0.0.0" : "127.0.0.1"}",
      "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", "#{dir}:#{dir}:ro",
      "--entrypoint", "sh", image, "-c",
      "sleep 5 && exec docker compose -f #{config} up -d --no-deps mission-control", timeout: 30)
    raise Refused, "can't change it right now: #{result.output.strip.lines.last&.strip}" unless result.success

    Installation.current.update!(port_open: open)
    Rails.logger.info("Port 3000 is #{open ? "opening to the network" : "closing to the network"}: Mission Control restarts")
  end

  def self.own_labels
    result = DockerCommand.run("inspect", "--format", "{{json .Config.Labels}}", Socket.gethostname, timeout: 5)
    labels = result.success ? JSON.parse(result.output) : {}
    labels.is_a?(Hash) ? labels : {}
  rescue JSON::ParserError
    {}
  end
  private_class_method :own_labels
end
