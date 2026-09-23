# A project Houston deploys, as last synced from its compose.yml by houston
# deploy. Facts only; secret values live in Secret.
class Project < ApplicationRecord
  has_many :hosts, class_name: "ProjectHost", dependent: :delete_all
  has_many :secrets, dependent: :delete_all

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
end
