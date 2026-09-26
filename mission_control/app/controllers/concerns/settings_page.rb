# The Settings page, one page of sections (docs/plans/mission-control-match-design.md,
# Batch 4). A section's form renders it again with its error, or with a new token.
module SettingsPage
  private
    def render_settings_page(status: :ok)
      @installation = Installation.current
      @locations = StorageLocation.where.not(verified_at: nil).order(:name)
      @tokens = ApiToken.order(:name)
      @updates = ServerUpdate.order(id: :desc).limit(10).to_a
      @newer = UpdateNotice.newer(HoustonVersion.current, @installation.latest_release)
      render "settings/pages/show", status:
    end
end
