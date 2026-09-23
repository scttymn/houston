require "test_helper"

class GitRemoteTest < ActiveSupport::TestCase
  test "the recorded host keys for a repo" do
    key = ->(_) { RepoLink.generate_key.last.split[0, 2].join(" ") }
    forgejo, github, custom = key.(1), key.(2), key.(3)
    Tempfile.create("known_hosts") do |f|
      f.write("forgejo #{forgejo}\ngithub.com #{github}\n[git.example.com]:2222 #{custom}\n")
      f.flush
      ENV["HOUSTON_KNOWN_HOSTS"] = f.path
      assert_equal [ "forgejo #{forgejo}" ], GitRemote.known_hosts_for("git@forgejo:houston/spike.git")
      assert_equal [ "github.com #{github}" ], GitRemote.known_hosts_for("ssh://git@github.com/sevenmoons/garage.git")
      assert_equal [ "[git.example.com]:2222 #{custom}" ], GitRemote.known_hosts_for("ssh://git@git.example.com:2222/x/y.git")
      assert_equal [], GitRemote.known_hosts_for("https://github.com/basecamp/kamal.git")
      assert_equal [], GitRemote.known_hosts_for("git@unknown.example:x/y.git")
    end
  ensure
    ENV.delete("HOUSTON_KNOWN_HOSTS")
  end
end
