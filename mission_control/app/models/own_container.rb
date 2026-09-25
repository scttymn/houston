# Mission Control's own container, as Docker sees it. The installer starts it
# with compose, whose labels say where its compose.yml and directory are;
# without them, it wasn't started by the installer, and Mission Control
# can't restart or update itself (PortSwitch, ServerUpdate).
module OwnContainer
  CONFIG_FILES = "com.docker.compose.project.config_files"
  WORKING_DIR = "com.docker.compose.project.working_dir"

  # {} when Docker can't say.
  def self.labels
    result = DockerCommand.run("inspect", "--format", "{{json .Config.Labels}}", Socket.gethostname, timeout: 5)
    labels = result.success ? JSON.parse(result.output) : {}
    labels.is_a?(Hash) ? labels : {}
  rescue JSON::ParserError
    {}
  end
end
