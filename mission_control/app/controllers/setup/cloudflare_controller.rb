# First-run step 2.
class Setup::CloudflareController < ApplicationController
  skip_before_action :require_setup
  before_action :closed_once_connected

  def show
    @setup = CloudflareSetup.new(base_domain: Installation.current.base_domain)
  end

  def create
    @setup = CloudflareSetup.new(params.expect(cloudflare: [ :base_domain, :api_token ]))
    if @setup.save
      redirect_to root_path
    else
      render :show, status: :unprocessable_entity
    end
  end

  private
    def closed_once_connected
      redirect_to root_path if Installation.connected?
    end
end
