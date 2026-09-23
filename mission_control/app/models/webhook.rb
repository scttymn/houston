# Is this push really from the repo's git host? Any one of: an HMAC-SHA256
# of the body with the project's secret (GitHub's X-Hub-Signature-256, which
# Gitea and Forgejo send too, or their own hex headers), or the secret itself
# as a token (GitLab, Houston). Constant-time comparisons throughout.
module Webhook
  HMAC_HEADERS = %w[X-Gitea-Signature X-Forgejo-Signature].freeze
  TOKEN_HEADERS = %w[X-Gitlab-Token X-Houston-Token].freeze

  def self.verified?(headers, body, secret)
    return false if secret.blank?

    expected = OpenSSL::HMAC.hexdigest("SHA256", secret, body)
    signatures = [ headers["X-Hub-Signature-256"].to_s.delete_prefix("sha256="), *HMAC_HEADERS.map { |h| headers[h] } ]
    tokens = TOKEN_HEADERS.map { |h| headers[h] }
    signatures.compact_blank.any? { |sig| ActiveSupport::SecurityUtils.secure_compare(sig.downcase, expected) } ||
      tokens.compact_blank.any? { |token| ActiveSupport::SecurityUtils.secure_compare(token, secret) }
  end
end
