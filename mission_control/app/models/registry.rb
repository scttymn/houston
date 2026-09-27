require "net/http"

# Houston's own registry (the installer's registry service, localhost:5000 on
# the host): what deleting a project needs of it (docs/plans/delete-project.md,
# Batch 7). Deleting a manifest by its digest removes every tag on it; the
# space is freed later, by RegistryCleanup.
class Registry
  class Error < StandardError; end

  MANIFEST_TYPES = %w[
    application/vnd.docker.distribution.manifest.v2+json
    application/vnd.oci.image.manifest.v1+json
    application/vnd.oci.image.index.v1+json
    application/vnd.docker.distribution.manifest.list.v2+json
  ].freeze
  CONFIG = "/etc/distribution/config.yml"
  TIMEOUT = 10

  def self.url = ENV.fetch("HOUSTON_REGISTRY_URL", "http://registry:5000")

  # The installer's Compose project, whose registry service this is.
  def self.compose_project = ENV.fetch("HOUSTON_COMPOSE_PROJECT", "houston")

  # Deletes every manifest of repository name; returns how many. Already
  # gone (the repository, or a manifest) counts as done.
  def delete_repository(name)
    listed = request(Net::HTTP::Get, "/v2/#{name}/tags/list")
    return 0 if listed.code == "404"
    raise Error, "listing #{name}'s tags: #{words(listed)}" unless listed.code == "200"

    tags = JSON.parse(listed.body)["tags"].to_a
    digests = tags.filter_map do |tag|
      head = request(Net::HTTP::Head, "/v2/#{name}/manifests/#{tag}", accept: MANIFEST_TYPES.join(", "))
      next if head.code == "404"
      raise Error, "reading #{name}:#{tag}: #{words(head)}" unless head.code == "200" && head["Docker-Content-Digest"].present?
      head["Docker-Content-Digest"]
    end.uniq
    digests.each do |digest|
      deleted = request(Net::HTTP::Delete, "/v2/#{name}/manifests/#{digest}")
      raise Error, "deleting #{name}@#{digest}: #{words(deleted)}" unless deleted.code.in?(%w[202 404])
    end
    digests.size
  rescue JSON::ParserError
    raise Error, "#{name}'s tag list wasn't JSON"
  end

  # The registry's container id, or nil.
  def container
    ran = DockerCommand.run("ps", "-q", "--filter", "label=com.docker.compose.project=#{self.class.compose_project}",
                            "--filter", "label=com.docker.compose.service=registry", timeout: 10)
    ran.success ? ran.output.split.first : nil
  end

  # Whether the installer turned deletes on (without them a DELETE answers
  # 405, and an unknown digest 404 either way, so the API can't say).
  def deletes_enabled?
    id = container or return false
    ran = DockerCommand.run("inspect", "--format", "{{json .Config.Env}}", id, timeout: 10)
    ran.success && JSON.parse(ran.output).include?("REGISTRY_STORAGE_DELETE_ENABLED=true")
  rescue JSON::ParserError
    false
  end

  private
    def request(verb, path, accept: nil)
      uri = URI("#{self.class.url}#{path}")
      request = verb.new(uri)
      request["Accept"] = accept if accept
      Net::HTTP.start(uri.host, uri.port, open_timeout: TIMEOUT, read_timeout: TIMEOUT) { |http| http.request(request) }
    rescue Net::OpenTimeout, Net::ReadTimeout, SocketError, SystemCallError => e
      raise Error, "couldn't reach the registry (#{e.class.name.demodulize})"
    end

    def words(response)
      codes = (JSON.parse(response.body.to_s)["errors"] rescue nil).to_a.filter_map { |e| e["code"] }
      [ response.code, codes.join(", ").presence ].compact.join(": ")
    end
end
