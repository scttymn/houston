# Finds the SQLite databases in the app's volumes and copies each one
# safely, for a backup. Runs in a helper container from Mission Control's
# image (plain Ruby and the sqlite3 CLI, no Rails), with each volume at
# <data>/<volume> and the staging volume at <out>:
#
#   ruby lib/backup/sqlite.rb /data /out
#
# For each database (a regular file of at least 512 bytes starting with the
# SQLite header; links aren't followed) it writes <out>/sqlite/<n>.sqlite3
# with `sqlite3 <file> .backup`, which is consistent while the app writes.
# <out>/.houston/exclude lists each live file and its -wal, -shm and
# -journal as literal restic patterns, so the file copy leaves them out.
#
# The last line of output is JSON: {"sqlite": [{volume, path, file}],
# "warnings": [...], "errors": [{volume, path, message}]}. The exit status
# is 1 when a database couldn't be copied.
require "json"
require "fileutils"
require "open3"
require "find"

HEADER = "SQLite format 3\0".b
MIN_SIZE = 512
MAX_DATABASES = 1000
MAX_WARNINGS = 20

data, out = ARGV
found = []
warnings = []
errors = []
unreadable = 0

Dir.children(data).sort.each do |volume|
  root = File.join(data, volume)
  next unless File.directory?(root)

  Find.find(root) do |path|
    stat = File.lstat(path)
    next unless stat.file? && stat.size >= MIN_SIZE
    next unless File.open(path, "rb") { |f| f.read(HEADER.bytesize) } == HEADER

    found << [ volume, path ]
  rescue SystemCallError => e
    unreadable += 1
    warnings << "#{path.delete_prefix("#{data}/").scrub}: #{e.message}" if unreadable <= MAX_WARNINGS
  end
end
found.sort!
warnings << "#{unreadable - MAX_WARNINGS} more files couldn't be checked" if unreadable > MAX_WARNINGS

if found.size > MAX_DATABASES
  errors << { volume: "", path: "", message: "more than #{MAX_DATABASES} SQLite databases; Houston stops there" }
  found = []
end

FileUtils.mkdir_p([ File.join(out, "sqlite"), File.join(out, ".houston") ])
copied = []
patterns = []
found.each_with_index do |(volume, path), i|
  relative = path.delete_prefix("#{data}/#{volume}/")
  unless relative.valid_encoding?
    warnings << "#{volume}/#{relative.scrub}: its name isn't UTF-8, so it's backed up only as a file"
    next
  end

  file = "sqlite/#{i + 1}.sqlite3"
  output, status = Open3.capture2e("sqlite3", path, ".backup '#{File.join(out, file)}'")
  unless status.success? && output.strip.empty?
    errors << { volume:, path: relative, message: output.strip.empty? ? "sqlite3 exited #{status.exitstatus}" : output.strip }
    next
  end
  copied << { volume:, path: relative, file: }

  if path.match?(/[\r\n]/)
    warnings << "#{volume}/#{relative}: its name has a line break, so its live file is backed up as well"
  else
    literal = path.delete_prefix(data).then { |p| "/data#{p}" }.gsub(/[\\*?\[]/) { "\\#{$&}" }
    patterns.concat([ literal, "#{literal}-wal", "#{literal}-shm", "#{literal}-journal" ])
  end
end

File.write(File.join(out, ".houston/exclude"), patterns.map { |p| "#{p}\n" }.join)
puts JSON.generate(sqlite: copied, warnings:, errors:)
exit(errors.empty? ? 0 : 1)
