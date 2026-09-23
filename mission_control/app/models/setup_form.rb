# First-run step 1: the setup code plus the admin's email and password.
class SetupForm
  include ActiveModel::Model
  include ActiveModel::Attributes

  attribute :code, :string
  attribute :email_address, :string
  attribute :password, :string
  attribute :password_confirmation, :string

  validates :email_address, presence: true, format: { with: URI::MailTo::EMAIL_REGEXP, message: "doesn't look like an email address" }
  validates :password, length: { minimum: 12, message: "needs at least 12 characters" }, confirmation: { message: "doesn't match" }
  validate :code_matches

  # Creates the admin and returns it, or nil with errors set. The code is used
  # up in the same transaction, so a failed save leaves it valid and a second
  # concurrent setup can't create a second admin.
  def save
    return unless valid?

    user = User.new(email_address:, password:)
    User.transaction do
      unless @setup_code.consume!
        errors.add(:base, "Setup was already completed. Sign in instead.")
        raise ActiveRecord::Rollback
      end
      user.save!
    end
    user if user.persisted?
  end

  private
    def code_matches
      @setup_code = SetupCode.matching(code)
      errors.add(:code, "doesn't match the one the installer printed") unless @setup_code
    end
end
