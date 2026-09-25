class SessionsController < ApplicationController
  allow_unauthenticated_access only: %i[ new create ]
  # From one address: 10 attempts in 3 minutes. Through the tunnel, also at
  # most EVERYONE failed ones from every address together, so many addresses
  # can't guess without limit. Only failures count, and the server itself
  # (ssh -L to port 3000) is never capped this way, so the admin can't be
  # locked out.
  EVERYONE = 50
  WINDOW = 3.minutes
  rate_limit to: 10, within: WINDOW, only: :create, with: -> { redirect_to sign_in_path, alert: "Try again later." }

  def new
  end

  def create
    return redirect_to(sign_in_path, alert: "Try again later.") if through_tunnel? && failures >= EVERYONE

    if user = User.authenticate_by(params.permit(:email_address, :password))
      start_new_session_for user
      redirect_to after_authentication_url
    else
      Rails.cache.increment(failures_key, 1, expires_in: WINDOW * 2) if through_tunnel?
      redirect_to sign_in_path, alert: "Try another email address or password."
    end
  end

  def destroy
    terminate_session
    redirect_to sign_in_path, status: :see_other
  end

  private
    def failures_key = "sign-in:failed:#{Time.current.to_i / WINDOW.to_i}"
    def failures = Rails.cache.read(failures_key, raw: true).to_i
end
