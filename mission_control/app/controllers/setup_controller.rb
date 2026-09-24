# First-run setup. Open only until the admin exists.
class SetupController < ApplicationController
  allow_unauthenticated_access
  skip_before_action :require_setup
  before_action :closed_once_set_up
  rate_limit to: 10, within: 3.minutes, only: :create, with: -> { head :too_many_requests }

  def show
    @form = SetupForm.new
  end

  def create
    @form = SetupForm.new(params.expect(setup: [ :code, :email_address, :password, :password_confirmation ]))
    if (user = @form.save)
      start_new_session_for user
      redirect_to root_path
    else
      render :show, status: :unprocessable_entity
    end
  end

  private
    def closed_once_set_up
      redirect_to(authenticated? ? root_path : sign_in_path) if User.exists?
    end
end
