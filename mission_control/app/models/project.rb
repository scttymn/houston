# A project Houston deploys, as last synced from its compose.yml by houston
# deploy. Facts only; secret values live in Secret.
class Project < ApplicationRecord
  has_many :hosts, class_name: "ProjectHost", dependent: :delete_all
  has_many :secrets, dependent: :delete_all
  has_many :deploys, dependent: :delete_all
  has_many :backup_runs, dependent: :delete_all
  has_many :project_volumes, dependent: :delete_all

  encrypts :deploy_key_private, :webhook_secret

  # <name>.<base>, the app's default host.
  def host(installation = Installation.current)
    "#{name}.#{installation.base_domain}"
  end

  # Container-name prefixes this project owns on the server: its own name
  # (Kamal's app containers and service label) and <name>-<service> for
  # each accessory.
  def host_names
    [ name ] + (services - [ app_service ]).map { |service| "#{name}-#{service}" }
  end

  # Required variables the file references that have no value yet.
  def missing_secrets
    have = secrets.select { |s| s.value.present? }.map(&:key)
    variables.select { |v| v["required"] }.map { |v| v["name"] } - have
  end

  def variable?(key)
    variables.any? { |v| v["name"] == key }
  end

  def accessories = services - [ app_service ]

  # Where this project's backups go: the default location, once setup's
  # storage step is finished.
  def backup_location = StorageLocation.where(default: true).where.not(acknowledged_at: nil).first

  # Each named volume of the app with where it lives: [volume, ProjectVolume or nil].
  def volume_rows
    chosen = project_volumes.includes(:location).index_by(&:name)
    volumes.map { |v| [ v, chosen[v["name"]] ] }
  end

  # Services a backup doesn't hold (they start empty after a restore).
  def not_backed_up = accessories - databases.map { |d| d["service"] }

  def latest_deploy = deploys.summary.order(number: :desc).first
  def running_deploy = deploys.summary.where(status: "go").order(number: :desc).first

  # :in_flight, :no_go or :go from the latest deploy; :standby before any.
  def status
    latest_deploy&.status&.to_sym || :standby
  end

  def deploy_rule_words
    if deploy_rule["on"] == "tag"
      "Tags matching #{deploy_rule["tags"].presence || "v*"}"
    else
      "Every commit to #{deploy_rule["branch"].presence || "main"}"
    end
  end
end
