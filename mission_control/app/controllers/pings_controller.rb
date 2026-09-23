# Mission Control GETs https://admin.<base>/ping to learn whether Cloudflare
# routes that name to this install yet (SystemStatus.admin_route). Like /up,
# it skips sign-in and setup, and sets no cookie.
class PingsController < ActionController::Base
  def show
    render plain: Installation.identity
  end
end
