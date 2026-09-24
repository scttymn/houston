require "test_helper"
require_relative "../support/fake_git"
require_relative "../support/project_helpers"

class ProjectLinksTest < ActionDispatch::IntegrationTest
  include FakeGitHelper
  include ProjectHelpers

  URL = "git@github.com:sevenmoons/garage.git"
  HEADS = "4be21c0aa11b2c3d4e5f60718293a4b5c6d7e8f9\trefs/heads/main\n9e41a07000000000000000000000000000000000\trefs/heads/dev\n"

  setup { sign_in_as users(:one) }

  def responder(ls_remote: git_ok(HEADS), clone: git_ok, inspect: git_ok(garage_inspection.to_json))
    lambda do |args, _env|
      case args
      in [ "git", "ls-remote", * ] then ls_remote
      in [ "git", "clone", * ] then clone
      in [ "git", "-C", _, "rev-parse", "HEAD" ] then git_ok("4be21c0aa11b2c3d4e5f60718293a4b5c6d7e8f9\n")
      in [ "git", "-C", * ] then git_ok
      in [ _, "-f", _, "inspect", "--json" ] then inspect
      end
    end
  end

  def check(url = URL) = post(link_access_path, params: { repo_url: url })
  def read(branch: "main", compose_path: "compose.yml") = post(link_read_path, params: { branch:, compose_path: })

  # Check access and Read compose.yml answer with the page itself (200), which
  # Turbo drops after a form post (it wants a redirect), so a browser showed
  # nothing. These forms submit as plain HTML instead.
  test "the houston init hint is one sentence, not three rows" do
    use_fake_git(FakeGit.new(&responder)) do
      check
      assert_select ".panel__row--muted > span", /No x-houston block yet\? Run houston init in the repo and push it first\./
    end
  end

  test "the forms that answer in place submit without Turbo" do
    use_fake_git(FakeGit.new(&responder)) do
      get link_path
      assert_select "form[action='#{link_access_path}'][data-turbo='false']"
      check
      assert_select "form[action='#{link_read_path}'][data-turbo='false']"
    end
  end

  test "hostile repo input never reaches git" do
    use_fake_git(FakeGit.new(&responder)) do |git|
      [ "file:///etc", "ext::sh -c id", "/srv/repo", "-oProxyCommand=id", "https://x/y.git --upload-pack=id",
        "https://user:token@github.com/x/y.git", "", "git@github.com:-oProxyCommand=id" ].each do |url|
        check(url)
        assert_response :unprocessable_entity, url.inspect
        assert_select ".field__error", /repo URL/i
      end
      assert_empty git.calls

      check
      calls = git.calls.size
      [ { branch: "-x" }, { branch: "a..b" }, { branch: "a b" }, { compose_path: "/etc/passwd" },
        { compose_path: "../x.yml" }, { compose_path: "deploy/../../x.yml" }, { compose_path: "compose.txt" } ].each do |bad|
        read(**bad)
        assert_response :unprocessable_entity, bad.inspect
      end
      assert_equal calls, git.calls.size, "git ran for a hostile branch or path"
    end
  end

  test "checking access to the repo" do
    use_fake_git(FakeGit.new(&responder)) do |git|
      check
      assert_response :success
      link = RepoLink.sole
      assert_select ".deploy-key", link.deploy_key_public
      assert_select ".check", /GO.*Houston can read the repo/m

      call = git.calls.last
      assert_equal [ "git", "ls-remote", "--heads", "--", URL ], call.args
      ssh = call.env.fetch("GIT_SSH_COMMAND")
      %w[-o\ IdentitiesOnly=yes -o\ BatchMode=yes -o\ StrictHostKeyChecking=accept-new -o\ ConnectTimeout=10].each { |opt| assert_includes ssh, opt }
      assert_match(/UserKnownHostsFile=\S*known_hosts/, ssh)
      assert_equal [ "0", "ssh:https" ], call.env.values_at("GIT_TERMINAL_PROMPT", "GIT_ALLOW_PROTOCOL")
      key_path = ssh[/-i (\S+)/, 1]
      assert_not File.exist?(key_path), "the deploy key's temp file is left behind"

      check
      assert_equal 1, RepoLink.count, "checking again reuses the draft and its key"
    end

    use_fake_git(FakeGit.new(&responder(ls_remote: git_ok("9e41a07000000000000000000000000000000000\trefs/heads/dev\n")))) do
      check
      assert_select ".check", /NO-GO.*main/m
    end

    denied = "git@github.com: Permission denied (publickey).\r\nfatal: Could not read from remote repository.\n"
    use_fake_git(FakeGit.new(&responder(ls_remote: git_failure(denied)))) do
      check
      assert_select ".check", /NO-GO.*Permission denied \(publickey\)/m
      assert_select ".check", /deploy key/
    end

    changed = "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n"
    use_fake_git(FakeGit.new(&responder(ls_remote: git_failure(changed)))) do
      check
      assert_select ".check", /NO-GO.*host key changed/m
    end
  end

  test "reading compose.yml" do
    use_fake_git(FakeGit.new(&responder)) do |git|
      check
      read
      assert_response :success
      clone = git.calls.find { |c| c.args[1] == "clone" }.args
      assert_equal [ "git", "clone", "--depth", "1", "--single-branch", "--branch", "main", "--no-tags", "--filter=blob:none", "--no-checkout", "--", URL ], clone[0, 12]
      tmp = clone.last
      assert git.calls.any? { |c| c.args == [ "git", "-C", tmp, "checkout", "HEAD", "--", "compose.yml" ] }
      assert git.calls.any? { |c| c.args[1..] == [ "-f", File.join(tmp, "compose.yml"), "inspect", "--json" ] }
      assert_not Dir.exist?(tmp), "the checkout is left behind"

      assert_select ".found", /main @ 4be21c0/
      assert_select ".found", /garage → garage\.svnmns\.com/
      assert_select ".found", /rideclubgarage\.com/
      assert_select ".found", /Port 3000 · health \/up · 1 CPU · 1 GB/
      assert_select ".found", /db · postgres:17/
      assert_select ".found", /Every commit to main · tests run first/
      assert_select ".found", /daily 03:00.*pgdata, storage/m
      assert_select ".link-secrets", /POSTGRES_PASSWORD/
      assert_select "button", /Save/
    end

    problems = "compose.yml: x-houston.health: must start with /\n"
    use_fake_git(FakeGit.new(&responder(inspect: git_failure(problems)))) do |git|
      read
      assert_response :unprocessable_entity
      assert_select ".problems", /x-houston\.health: must start with \//
      assert_select "button[disabled]", /Save/
      assert_not Dir.exist?(git.calls.find { |c| c.args[1] == "clone" }.args.last)
    end
  end

  test "saving links the project" do
    use_fake_git(FakeGit.new(&responder)) do
      check
      read
      post link_path
      assert_redirected_to project_path("garage")
      project = Project.find_by!(name: "garage")
      assert_equal [ URL, "main", "compose.yml" ], [ project.repo_url, project.branch, project.compose_path ]
      assert_match(/\Assh-ed25519 /, project.deploy_key_public)
      assert_operator project.webhook_secret.length, :>=, 43
      raw = Project.connection.select_values("SELECT webhook_secret, deploy_key_private FROM projects WHERE id = #{project.id}").join
      assert_not_includes raw, project.webhook_secret
      assert_not_includes raw, "OPENSSH PRIVATE KEY"
      assert_equal %w[garage garage-db], project.hosts.pluck(:name).sort
      assert_equal 0, RepoLink.count
      secret = project.webhook_secret

      # Linking the same repo again is fine and keeps the webhook secret.
      check
      read
      post link_path
      assert_redirected_to project_path("garage")
      assert_equal secret, project.reload.webhook_secret

      # Another repo can't take the name.
      check("git@github.com:someone/garage.git")
      read
      post link_path
      assert_response :unprocessable_entity
      assert_match(/already linked to #{Regexp.escape(URL)}/, response.body)
    end
  end

  # The design's AddProject: step 01 on the left; 02-04 and Save on the right.
  test "the steps sit where the design has them" do
    use_fake_git(FakeGit.new(&responder)) do
      get link_path
      assert_select ".link-grid > .link-repo .panel__title", /01\s*Link the repo/i
      assert_select ".link-grid > .link-side", /Read compose\.yml to see what Houston found/
      assert_select ".link-secrets", 0

      check
      read
      titles = css_select(".link-grid > .link-side .panel__title").map { |t| t.text.squish }
      assert_equal [ "02 What Houston found", "03 Secrets", "04 Connect pushes" ], titles
      assert_equal %w[Save Deploy], css_select(".link-side .link-save button").map { |b| b.text.strip }
      assert_select ".link-side .link-save a", "Cancel"
    end
  end

  test "secrets set on Add project are saved with it" do
    use_fake_git(FakeGit.new(&responder)) do
      check
      read
      assert_select ".link-secrets [data-secret=POSTGRES_PASSWORD] input[type=password][placeholder=Required]"
      assert_select ".link-secrets [data-secret=SENTRY_DSN] input[type=password][placeholder=Optional]"
      post link_path, params: { secrets: { POSTGRES_PASSWORD: "pg-secret-123", SENTRY_DSN: "", NOT_IN_THE_FILE: "x" } }
      assert_redirected_to project_path("garage")
    end
    project = Project.find_by!(name: "garage")
    assert_equal({ "POSTGRES_PASSWORD" => "pg-secret-123" }, project.secrets.to_h { |s| [ s.key, s.value ] })
    follow_redirect!
    assert_not_includes response.body, "pg-secret-123"
  end

  test "a bad secret refuses the whole save" do
    use_fake_git(FakeGit.new(&responder)) do
      check
      read
      post link_path, params: { secrets: { POSTGRES_PASSWORD: "ok-value", SENTRY_DSN: 'C:\\data' } }
      assert_response :unprocessable_entity
      assert_select ".link-secrets [data-secret=SENTRY_DSN] .field__error", /backslash/
      assert_not_includes response.body, "ok-value"
    end
    assert_nil Project.find_by(name: "garage")
    assert_equal 0, Secret.count
    assert_equal 1, RepoLink.count
  end

  test "the webhook secret is shown before saving and kept" do
    use_fake_git(FakeGit.new(&responder)) do
      check
      read
      shown = css_select(".link-webhook [data-webhook-secret]").first.text.strip
      assert_operator shown.length, :>=, 43
      assert_select ".link-webhook", %r{https://hooks\.svnmns\.com/garage}
      post link_path
      assert_equal shown, Project.find_by!(name: "garage").webhook_secret

      check
      read
      assert_select ".link-webhook [data-webhook-secret]", shown
      post link_path
      assert_equal shown, Project.find_by!(name: "garage").webhook_secret
    end
  end

  test "Deploy saves then deploys; a failed read still saves" do
    use_fake_git(FakeGit.new(&responder)) do
      check
      read
      post link_path, params: { deploy: "1" }
    end
    project = Project.find_by!(name: "garage")
    assert_redirected_to project_deploy_path("garage", 1)
    assert_equal [ "4be21c0aa11b2c3d4e5f60718293a4b5c6d7e8f9", "queued" ], project.deploys.sole.values_at(:sha, :status)

    project.destroy!
    use_fake_git(FakeGit.new(&responder)) do
      check
      read
    end
    use_fake_git(FakeGit.new(&responder(ls_remote: git_failure("fatal: Could not read from remote repository.")))) { post link_path, params: { deploy: "1" } }
    assert_redirected_to project_path("garage")
    assert Project.find_by(name: "garage")
    follow_redirect!
    assert_select ".notice--nogo", /Could not read from remote repository/
  end

  test "volumes start on local disk; placement is on the project page" do
    use_fake_git(FakeGit.new(&responder)) do
      check
      read
      assert_select "select", 0
      post link_path, params: { locations: { storage: "unas-nfs" } }
      assert_redirected_to project_path("garage")
    end
    assert_empty Project.find_by!(name: "garage").project_volumes.where.not(location_id: nil)
  end

  test "saving refuses a container-name clash and links a project houston deploy registered" do
    make_project("garage-db")
    use_fake_git(FakeGit.new(&responder)) do
      check
      read
      post link_path
      assert_response :unprocessable_entity
      assert_match(/garage-db belongs to project garage-db/, response.body)
      assert_nil Project.find_by(name: "garage")
    end

    Project.find_by!(name: "garage-db").destroy!
    registered = make_project("garage", services: %w[app db])
    use_fake_git(FakeGit.new(&responder)) do
      check
      read
      post link_path
      assert_redirected_to project_path("garage")
      assert_equal URL, registered.reload.repo_url
    end
  end

  test "linking needs the admin" do
    sign_out
    use_fake_git(FakeGit.new(&responder)) do |git|
      get link_path
      assert_redirected_to new_session_path
      check
      assert_redirected_to new_session_path
      read
      post link_path
      assert_empty git.calls
      assert_equal 0, RepoLink.count
    end
  end

  test "save needs a read preview" do
    use_fake_git(FakeGit.new(&responder)) do
      post link_path
      assert_response :unprocessable_entity
      assert_match(/read the file first/i, response.body)

      check
      post link_path
      assert_response :unprocessable_entity
      assert_nil Project.find_by(name: "garage")
    end
  end
end
