require "net/http"

# The latest Houston release, from GitHub on a schedule (LatestReleaseJob),
# kept on the Installation so the flight board never waits on GitHub
# (docs/plans/update-available.md). GitHub's "latest" leaves prereleases out.
# A failed check is logged and keeps what we knew.
module LatestRelease
  TIMEOUT = 10

  def self.repo = ENV["HOUSTON_REPO"].presence || "scttymn/houston"

  # The latest release's tag, or nil when the check failed.
  def self.check!(installation = Installation.current)
    return nil unless installation.persisted?

    tag, url = fetch
    before = installation.latest_release
    installation.update!(latest_release: tag, latest_release_url: url, latest_release_checked_at: Time.current)
    FlightBoard.refresh! if tag != before
    tag
  rescue StandardError => e
    Rails.logger.warn("houston: couldn't check the latest release: #{e.class}: #{e.message}")
    nil
  end

  # A check someone asked for (Check now on Houston's page, houston update --check): what
  # it found, in words, or nil when GitHub didn't answer.
  def self.check_and_say
    tag = check! or return nil
    newer = UpdateNotice.newer(HoustonVersion.current, tag)
    newer ? "#{newer} is out." : "#{tag} is the latest."
  end

  def self.fetch
    uri = URI("https://api.github.com/repos/#{repo}/releases/latest")
    response = Net::HTTP.start(uri.host, uri.port, use_ssl: true, open_timeout: TIMEOUT, read_timeout: TIMEOUT) do |http|
      http.get(uri.path, "Accept" => "application/vnd.github+json", "User-Agent" => "houston/#{HoustonVersion.current}")
    end
    raise "GitHub answered #{response.code}" unless response.is_a?(Net::HTTPSuccess)

    release = JSON.parse(response.body)
    tag, url = release["tag_name"].to_s, release["html_url"].to_s
    raise "#{tag.inspect} isn't a release tag" unless tag.match?(HoustonVersion::TAG)
    raise "#{url.inspect} isn't this repo's release page" unless url.start_with?("https://github.com/#{repo}/releases/")
    [ tag, url ]
  end
  private_class_method :fetch
end
