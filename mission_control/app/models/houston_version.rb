# Which Houston this is (docs/plans/releases.md): a release's tag, baked into
# the published image; a checkout's commit, when the installer built the image
# from source; or dev. Only a tag or a hex commit counts.
module HoustonVersion
  TAG = /\Av\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?\z/
  SHA = /\A\h{7,40}\z/

  def self.current
    tag, sha = ENV["HOUSTON_VERSION"].to_s, ENV["HOUSTON_SOURCE_SHA"].to_s
    return tag if tag.match?(TAG)
    return "source #{sha[0, 7]}" if sha.match?(SHA)
    "dev"
  end
end
