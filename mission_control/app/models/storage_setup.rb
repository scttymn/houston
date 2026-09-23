# First-run step 3: create the default backup location and prove restic can
# write there. The password is saved (encrypted) before `restic init`, so a
# rerun after a crash reuses it and can always open what it created.
class StorageSetup
  include ActiveModel::Model
  include ActiveModel::Attributes

  %i[kind name nfs_server nfs_export local_path s3_endpoint s3_bucket s3_access_key_id
     s3_secret_access_key b2_bucket b2_key_id b2_application_key].each { |a| attribute a, :string }

  HOST = /\A(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\z/i
  IPV4 = /\A(?:\d{1,3}\.){3}\d{1,3}\z/
  ABSOLUTE = %r{\A/(?!.*(?:\A|/)\.\.(?:/|\z))}
  BUCKET = /\A[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]\z/
  ALREADY = /already (exists|initialized)/i

  Check = Data.define(:ok, :label)
  attr_reader :checks, :location

  validates :kind, inclusion: { in: StorageLocation::KINDS, message: "must be nfs, local, s3 or b2" }
  validates :name, format: { with: /\A[a-z][a-z0-9-]{0,62}\z/, message: "must be lowercase letters, digits and dashes" }
  with_options if: -> { kind == "nfs" } do
    validate { errors.add(:nfs_server, "must be a host name or IP address") unless nfs_server.to_s.match?(HOST) || nfs_server.to_s.match?(IPV4) }
    validates :nfs_export, format: { with: ABSOLUTE, message: "must be an absolute path like /volume1/houston" }
  end
  validates :local_path, format: { with: ABSOLUTE, message: "must be an absolute path on the server" }, if: -> { kind == "local" }
  with_options if: -> { kind == "s3" } do
    validates :s3_bucket, format: { with: BUCKET, message: "must be a bucket name" }
    validates :s3_access_key_id, :s3_secret_access_key, presence: true
    validates :s3_endpoint, format: { with: %r{\Ahttps://[^\s/]+/?\z}, message: "must be an https:// endpoint" }, allow_blank: true
  end
  with_options if: -> { kind == "b2" } do
    validates :b2_bucket, format: { with: BUCKET, message: "must be a bucket name" }
    validates :b2_key_id, :b2_application_key, presence: true
  end

  def initialize(...)
    super
    @checks = []
  end

  def save
    return false unless valid?

    @location = StorageLocation.find_or_initialize_by(name:)
    if @location.acknowledged?
      errors.add(:name, "is already used by another storage location")
      return false
    end
    @location.assign_attributes(kind:, settings:, credentials:)
    @location.restic_password ||= SecureRandom.base58(40)
    @location.save!

    volume = @location.ensure_volume
    return fail!("Couldn't create the NFS volume: #{last_line(volume.output)}") unless volume.success

    init = @location.restic("init")
    if !init.success && init.output.match?(ALREADY)
      opened = @location.restic("cat", "config")
      return fail!("There's already a restic repository at #{@location.where_it_is}, and Houston's password doesn't open it (restic: #{last_line(opened.output)}). Pick another location, or remove that repository yourself.") unless opened.success
    elsif !init.success
      return fail!("restic couldn't write there: #{last_line(init.output)}")
    end

    @location.update!(verified_at: Time.current)
    true
  end

  private
    def settings
      case kind
      when "nfs" then { "server" => nfs_server, "export" => nfs_export }
      when "local" then { "path" => local_path }
      when "s3" then { "endpoint" => s3_endpoint.presence, "bucket" => s3_bucket }.compact
      when "b2" then { "bucket" => b2_bucket }
      end
    end

    def credentials
      case kind
      when "s3" then { "access_key_id" => s3_access_key_id, "secret_access_key" => s3_secret_access_key }
      when "b2" then { "key_id" => b2_key_id, "application_key" => b2_application_key }
      else {}
      end
    end

    def last_line(output)
      output.to_s.lines.map(&:strip).reject(&:empty?).last || "no output"
    end

    def fail!(label)
      @checks << Check.new(ok: false, label:)
      false
    end
end
