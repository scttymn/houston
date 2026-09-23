# The one-time code that proves whoever finishes first-run setup is the person
# who ran the installer. Only its bcrypt digest is stored. The installer mints
# one with `bin/rails houston:setup_code`; it stops working once the admin
# exists.
class SetupCode < ApplicationRecord
  has_secure_password :code, validations: false

  # No 0/O or 1/I, so a code read off a terminal can't be mistyped that way.
  ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789".chars.freeze

  # Replaces any previous code and returns the new one, e.g. "K7QM-2XHD".
  def self.issue!
    code = Array.new(8) { ALPHABET[SecureRandom.random_number(ALPHABET.size)] }.join
    transaction do
      delete_all
      create!(code: normalize(code))
    end
    code.insert(4, "-")
  end

  # The stored code matching what someone typed, or nil. Case and the dash
  # don't matter.
  def self.matching(typed)
    normalized = normalize(typed.to_s)
    return if normalized.length != 8
    all.find { |setup_code| setup_code.authenticate_code(normalized) }
  end

  def self.normalize(code)
    code.upcase.gsub(/[^A-Z0-9]/, "")
  end

  # Uses the code up. Exactly one request can: the one whose delete removes the
  # row (SQLite serializes writes). Returns false if another got there first.
  def consume!
    self.class.where(id: id).delete_all == 1
  end
end
