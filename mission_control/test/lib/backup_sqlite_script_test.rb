require "test_helper"
require "open3"

# lib/backup/sqlite.rb runs for real here: in production it runs the same
# way, in Mission Control's image, against the app's volumes.
class BackupSqliteScriptTest < ActiveSupport::TestCase
  SCRIPT = Rails.root.join("lib/backup/sqlite.rb").to_s

  setup do
    @dir = Dir.mktmpdir
    @data = File.join(@dir, "data")
    @out = File.join(@dir, "out")
    FileUtils.mkdir_p([ File.join(@data, "storage/nested/deep"), File.join(@data, "media"), @out ])
  end

  teardown do
    @live&.close
    FileUtils.rm_rf(@dir)
  end

  def make_db(path, rows)
    db = SQLite3::Database.new(path)
    db.execute("CREATE TABLE t (n INTEGER)")
    rows.times { |i| db.execute("INSERT INTO t VALUES (?)", [ i ]) }
    db
  end

  def rows(path) = SQLite3::Database.new(path, readonly: true).then { |db| db.get_first_value("SELECT count(*) FROM t").tap { db.close } }

  def run_script
    output, status = Open3.capture2e("ruby", SCRIPT, @data, @out)
    [ output, status ]
  end

  test "finds and copies SQLite databases" do
    # A WAL database whose newest rows are only in its -wal: the connection
    # stays open, and nothing checkpoints.
    live = File.join(@data, "storage/production.sqlite3")
    @live = make_db(live, 0)
    @live.execute("PRAGMA journal_mode=WAL")
    @live.execute("PRAGMA wal_autocheckpoint=0")
    40.times { |i| @live.execute("INSERT INTO t VALUES (?)", [ i ]) }
    assert File.size(live + "-wal") > 0

    hostile = File.join(@data, "storage", "a'b *[x].sqlite3")
    make_db(hostile, 3).close
    make_db(File.join(@data, "storage/nested/deep/q.db"), 5).close
    newline = File.join(@data, "media", "new\nline.sqlite3")
    make_db(newline, 2).close
    File.write(File.join(@data, "storage/notes.txt"), "not a database\n" * 100)
    File.write(File.join(@data, "media/fake.bin"), "SQLite format 3X" + "x" * 1000) # no NUL: not SQLite
    File.binwrite(File.join(@data, "media/tiny.sqlite3"), "SQLite format 3\0") # under 512 bytes: not a database
    File.symlink(live, File.join(@data, "media/link.sqlite3")) # links aren't followed

    output, status = run_script
    assert status.success?, output
    result = JSON.parse(output.lines.last)

    found = result["sqlite"].map { |s| [ s["volume"], s["path"] ] }
    assert_equal [ [ "media", "new\nline.sqlite3" ], [ "storage", "a'b *[x].sqlite3" ], [ "storage", "nested/deep/q.db" ], [ "storage", "production.sqlite3" ] ], found
    counts = result["sqlite"].to_h { |s| [ s["path"], rows(File.join(@out, s["file"])) ] }
    assert_equal({ "new\nline.sqlite3" => 2, "a'b *[x].sqlite3" => 3, "nested/deep/q.db" => 5, "production.sqlite3" => 40 }, counts)
    assert_equal (1..4).map { |n| "sqlite/#{n}.sqlite3" }, result["sqlite"].map { |s| s["file"] }

    # The exclude file: each live file, -wal, -shm and -journal, as literal
    # patterns. A name with a newline can't be a line: it stays in the file copy.
    patterns = File.read(File.join(@out, ".houston/exclude")).split("\n")
    expected = [ "/data/storage/a'b \\*\\[x].sqlite3", "/data/storage/nested/deep/q.db", "/data/storage/production.sqlite3" ]
                 .flat_map { |p| [ p, "#{p}-wal", "#{p}-shm", "#{p}-journal" ] }
    assert_equal expected.sort, patterns.sort
    assert_equal [ "media/new\nline.sqlite3: its name has a line break, so its live file is backed up as well" ], result["warnings"]
    assert_empty result["errors"]
  end

  test "no volumes, no databases" do
    FileUtils.rm_rf(@data)
    FileUtils.mkdir_p(@data)
    output, status = run_script
    assert status.success?, output
    assert_equal({ "sqlite" => [], "warnings" => [], "errors" => [] }, JSON.parse(output.lines.last))
    assert_equal "", File.read(File.join(@out, ".houston/exclude"))
  end

  test "a database that can't be copied fails the script" do
    File.binwrite(File.join(@data, "storage/broken.sqlite3"), "SQLite format 3\0" + "\xff".b * 4096)
    output, status = run_script
    assert_not status.success?
    result = JSON.parse(output.lines.last)
    assert_equal [ "storage" ], result["errors"].map { |e| e["volume"] }
    assert_equal "broken.sqlite3", result["errors"].first["path"]
    assert result["errors"].first["message"].present?
  end
end
