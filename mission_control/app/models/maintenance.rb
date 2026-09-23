# Houston's maintenance page for a project: the admin's switch (deploys and
# restores never touch it). On routes the project's hostnames to Mission
# Control at the tunnel, which serves the page; off routes them back. The
# database and the tunnel always agree: a toggle that Cloudflare refuses is
# rolled back. Toggles are serialized by a file lock, and each push is built
# from the database after its own change, so the last push holds them all.
module Maintenance
  class Failed < StandardError; end

  LOCK = Rails.root.join("storage/tunnel-routes.lock")

  def self.on!(project, by:, message:)
    toggle(project, maintenance_since: project.maintenance_since || Time.current, maintenance_by: by, maintenance_message: message.presence)
  end

  def self.off!(project)
    toggle(project, maintenance_since: nil, maintenance_by: nil, maintenance_message: nil)
  end

  def self.toggle(project, changes)
    installation = Installation.current
    raise Failed, "Cloudflare isn't connected, so there's no tunnel to route through" unless installation.connected? && installation.tunnel_id.present?

    File.open(LOCK, File::RDWR | File::CREAT, 0o600) do |lock|
      lock.flock(File::LOCK_EX)
      project.reload
      before = project.slice(*changes.keys)
      project.update!(changes)
      begin
        TunnelRoutes.push!(installation)
      rescue Cloudflare::Error => e
        project.update!(before)
        raise Failed, "Cloudflare said no: #{e.message}"
      end
    end
    project
  end
  private_class_method :toggle
end
