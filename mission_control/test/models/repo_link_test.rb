require "test_helper"

class RepoLinkTest < ActiveSupport::TestCase
  test "a new link gets its own ed25519 deploy key, private half encrypted" do
    link = RepoLink.start!("git@github.com:sevenmoons/garage.git")

    assert_match(/\Assh-ed25519 [A-Za-z0-9+\/=]+ houston@svnmns\.com\z/, link.deploy_key_public)
    assert_includes link.deploy_key_private, "BEGIN OPENSSH PRIVATE KEY"
    raw = RepoLink.connection.select_value("SELECT deploy_key_private FROM repo_links WHERE id = #{link.id}")
    assert_not_includes raw, "OPENSSH PRIVATE KEY"
    assert_equal [ "main", "compose.yml" ], [ link.branch, link.compose_path ]
    assert_not_equal link.deploy_key_public, RepoLink.start!("git@github.com:sevenmoons/other.git").deploy_key_public
  end

  test "drafts older than a day are deleted when a new one starts" do
    old = travel_to(2.days.ago) { RepoLink.start!("https://github.com/sevenmoons/old.git") }
    recent = RepoLink.start!("https://github.com/sevenmoons/recent.git")

    RepoLink.start!("https://github.com/sevenmoons/new.git")

    assert_nil RepoLink.find_by(id: old.id)
    assert RepoLink.find_by(id: recent.id)
  end
end
