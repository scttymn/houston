require "test_helper"

class StorageLocationTest < ActiveSupport::TestCase
  # docs/plans/security-fixes.md, M3: a moved tag can't change what runs.
  test "restic's image is pinned by digest" do
    assert_match %r{\Arestic/restic:[0-9.]+@sha256:[0-9a-f]{64}\z}, StorageLocation::RESTIC_IMAGE
  end
end
