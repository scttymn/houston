# A place restic can write backups (and, for nfs/local, where live volumes can
# live). Credentials and the restic password are encrypted at rest.
class StorageLocation < ApplicationRecord
  KINDS = %w[nfs local s3 b2].freeze
  RESTIC_IMAGE = "restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510"

  serialize :settings, coder: JSON
  serialize :credentials, coder: JSON
  encrypts :credentials, :restic_password

  validates :name, format: { with: /\A[a-z][a-z0-9-]{0,62}\z/ }, uniqueness: true
  validates :kind, inclusion: { in: KINDS }

  def self.default_ready?
    where(default: true).where.not(acknowledged_at: nil).exists?
  end

  # The location first-run setup is working on: verified, not yet finished.
  def self.setup_candidate
    where(acknowledged_at: nil).where.not(verified_at: nil).order(:id).last
  end

  # Can hold a project's live volumes (spec §9); s3 and b2 hold backups only.
  def live? = kind.in?(%w[nfs local])

  def holds_words = live? ? "live volumes · backups" : "backups"

  # Projects backing up here: their own target, or the default for those without one.
  def projects_using
    ids = Project.where(backup_location_id: id).pluck(:id)
    ids += Project.where(backup_location_id: nil).pluck(:id) if default? && acknowledged?
    ids.uniq.size
  end

  def last_write = BackupRun.where(location_id: id, status: "go").maximum(:finished_at)

  def verified? = verified_at.present?
  def acknowledged? = acknowledged_at.present?
  def settings = super || {}
  def credentials = super || {}

  def volume_name = "houston-storage-#{name}"

  def repository
    case kind
    when "nfs", "local" then "/repo"
    when "s3" then "s3:#{settings["endpoint"].presence&.chomp("/") || "https://s3.amazonaws.com"}/#{settings["bucket"]}/houston"
    when "b2" then "b2:#{settings["bucket"]}:houston"
    end
  end

  # How a choice of location reads in a menu: "unas (NFS, 10.0.1.20:/volume1/houston)".
  def choice_label = "#{name} (#{kind == "local" ? "local folder" : kind.upcase}, #{where_it_is})"

  def where_it_is
    case kind
    when "nfs" then "#{settings["server"]}:#{settings["export"]}"
    when "local" then settings["path"]
    else repository
    end
  end

  # Makes sure the NFS volume exists (reusing it on reruns).
  def ensure_volume
    return DockerCommand::Result.new(success: true, output: "") unless kind == "nfs"
    return DockerCommand::Result.new(success: true, output: "") if DockerCommand.run("volume", "inspect", volume_name).success

    DockerCommand.run("volume", "create", "--driver", "local",
                      "--opt", "type=nfs", "--opt", "o=addr=#{settings["server"]},rw,nfsvers=4",
                      "--opt", "device=:#{settings["export"]}", volume_name)
  end

  # restic's cache survives between runs (each run is a fresh container), so
  # a run doesn't re-read the whole repository index.
  RESTIC_CACHE = "houston-restic-cache:/root/.cache/restic"

  def restic(*command)
    DockerCommand.run(*restic_args(*command), env: restic_env)
  end

  # docker run arguments for a restic command against this location. name:
  # the container's; mounts: more -v values (what to back up).
  def restic_args(*command, name: nil, mounts: [])
    repo = case kind
    when "nfs" then [ "#{volume_name}:/repo" ]
    when "local" then [ "#{settings["path"]}:/repo" ]
    else []
    end
    [ "run", "--rm", *(name ? [ "--name", name ] : []), *restic_env.keys.flat_map { |k| [ "-e", k ] },
      *([ RESTIC_CACHE ] + repo + mounts).flat_map { |m| [ "-v", m ] }, RESTIC_IMAGE, *command ]
  end

  # The secrets, for the docker process's environment (never argv).
  def restic_env
    { "RESTIC_PASSWORD" => restic_password, "RESTIC_REPOSITORY" => repository }.merge(credential_env)
  end

  private
    def credential_env
      case kind
      when "s3" then { "AWS_ACCESS_KEY_ID" => credentials["access_key_id"], "AWS_SECRET_ACCESS_KEY" => credentials["secret_access_key"] }
      when "b2" then { "B2_ACCOUNT_ID" => credentials["key_id"], "B2_ACCOUNT_KEY" => credentials["application_key"] }
      else {}
      end
    end
end
