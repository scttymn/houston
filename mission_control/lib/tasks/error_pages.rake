desc "Write Mission Control's error pages (public/*.html) from lib/error_pages.rb and the patch"
task error_pages: :environment do
  ErrorPages.write
end
