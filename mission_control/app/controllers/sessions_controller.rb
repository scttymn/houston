class SessionsController < ApplicationController
  allow_unauthenticated_access only: %i[ new create ]
  # Attempts in 3 minutes: from one address, and from every address together
  # (so many addresses can't guess without limit).
  EVERYONE = 50
  rate_limit to: 10, within: 3.minutes, only: :create, with: -> { redirect_to sign_in_path, alert: "Try again later." }
  rate_limit to: EVERYONE, within: 3.minutes, by: -> { "everyone" }, name: "everyone", only: :create,
             with: -> { redirect_to sign_in_path, alert: "Try again later." }

  def new
  end

  def create
    if user = User.authenticate_by(params.permit(:email_address, :password))
      start_new_session_for user
      redirect_to after_authentication_url
    else
      redirect_to sign_in_path, alert: "Try another email address or password."
    end
  end

  def destroy
    terminate_session
    redirect_to sign_in_path, status: :see_other
  end
end
