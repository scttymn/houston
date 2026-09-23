# A value for one variable a project's compose.yml references. Kamal hands
# values to containers through docker env files, which can't carry a
# backslash, tab or line break unchanged, so those are refused here, where
# the admin can still do something about it.
class Secret < ApplicationRecord
  belongs_to :project
  encrypts :value

  UNCARRIABLE = /[\\\x00-\x1f\x7f]/

  # A value nobody needs to know (Generate): 64 random bytes, URL-safe
  # base64 (86 characters). Frameworks' keys want at least 64 bytes
  # (Phoenix's and Rails' secret_key_base); any password takes it too.
  def self.generated_value = SecureRandom.urlsafe_base64(64)

  validates :key, format: { with: /\A[A-Za-z_][A-Za-z0-9_]*\z/, message: "must be letters, digits and _, not starting with a digit" }
  validates :value, presence: { message: "needs a value" }
  validate :value_can_reach_the_container

  private
    def value_can_reach_the_container
      if value.to_s.match?(UNCARRIABLE)
        errors.add(:value, "can't contain a backslash, line break, tab or other control character: Kamal would change it on the way to the app. Base64-encode it instead.")
      end
    end
end
