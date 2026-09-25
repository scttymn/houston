require "test_helper"

class StorageSetupTest < ActiveSupport::TestCase
  # Security fixes, L11: the whole path is checked, not only its first line.
  test "a path is absolute, without .., on every line" do
    assert_match StorageSetup::ABSOLUTE, "/srv/backups"
    [ "srv/backups", "/srv/../etc", "/srv/backups\n/../etc", "/srv/backups\n" ].each do |path|
      assert_no_match StorageSetup::ABSOLUTE, path, path.inspect
    end
  end
end
