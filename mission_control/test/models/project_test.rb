require "test_helper"
require_relative "../support/project_helpers"

class ProjectTest < ActiveSupport::TestCase
  include ProjectHelpers

  # A domain that's also the default host (a copy's new name was the old
  # project's custom domain) is one host, not two (docs/plans/copy-project.md).
  test "hostnames are each listed once" do
    project = make_project("blog", domains: %w[blog.svnmns.com www.blog.example blog.svnmns.com])
    assert_equal %w[blog.svnmns.com www.blog.example], project.hostnames
  end
end
