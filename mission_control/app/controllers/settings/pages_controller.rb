# Settings: Cloudflare, the time zone, storage and API tokens, on one page.
class Settings::PagesController < ApplicationController
  include SettingsPage

  def show
    render_settings_page
  end
end
