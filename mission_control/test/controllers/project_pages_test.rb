require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_git"

class ProjectPagesTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeGitHelper

  setup do
    sign_in_as users(:one)
    @project = make_project("equip", services: %w[app db cache], domains: %w[equipping.com],
                            variables: [ { "name" => "RAILS_MASTER_KEY", "required" => true }, { "name" => "SENTRY_DSN", "required" => false },
                                         { "name" => "STRIPE_KEY", "required" => true } ])
  end

  test "a project's page" do
    12.times { |i| make_deploy(@project, i + 1, i == 4 ? "no_go" : "go", error: (i == 4 ? "release hook failed (exit 3)" : nil)) }

    get project_path("equip")
    assert_response :success
    assert_select "h1", /equip/i
    assert_select "a[href='https://equip.svnmns.com']"
    assert_select "a[href='https://equipping.com']"
    assert_select ".facts-strip", /Every commit to\s+main/
    assert_select ".facts-strip", /db.*cache|cache.*db/m
    assert_select ".facts-strip", /#{format("%040x", 12)[0, 7]}/
    assert_select ".history [data-deploy]", 10
    assert_match(/#12/, css_select(".history [data-deploy]").first.text)
    assert_select ".history a[href='/projects/equip/deploys/12']"

    get project_path("equip", page: 2)
    assert_select ".history [data-deploy]", 2
    assert_select ".history", /#2/
    assert_select ".history", /#1/

    get project_path("nope")
    assert_response :not_found
  end

  # The design's ProjectDetail: the header, the facts strip, then two rows of
  # two panels (docs/plans/mission-control-match-design.md, Batch 1).
  test "the header follows the design: chip beside the name, host chips, console box" do
    @project.update!(details: { "console" => "bin/rails console" }, domain_states: { "equipping.com" => { "state" => "DNS OK" } })
    make_deploy(@project, 1, "go")
    get project_path("equip")
    assert_select ".project-head__title h1", /equip/i
    assert_select ".project-head__title .state", "GO"
    assert_select ".host-chip", 2
    assert_select ".host-chip[href='https://equip.svnmns.com'][target=_blank]", /equip\.svnmns\.com/
    assert_select ".host-chip[href='https://equipping.com'] .domain-state", "DNS OK"
    assert_select ".console-box", /RUNS\s+bin\/rails console/
    assert_select ".console-box .console-box__command", "houston console --server"
    assert_select ".project-head form[action='/projects/equip/backups']", 0, "Create Snapshot lives in the Snapshots panel"
  end

  test "no console box without commands.console" do
    get project_path("equip")
    assert_select ".console-box", 0
  end

  test "the facts strip: sha, rule, resources, services, repo" do
    @project.update!(details: { "images" => { "db" => "postgres:17", "cache" => "redis:7" }, "cpus" => "2", "memory" => "2 GB" })
    make_deploy(@project, 3, "go")
    get project_path("equip")
    assert_select ".facts-strip > div", 5
    assert_select ".facts-strip > div:nth-child(1)", /RUNNING SHA\s+#{format("%040x", 3)[0, 7]}/
    assert_select ".facts-strip > div:nth-child(2)", /DEPLOY RULE\s+Every commit to\s+main/
    assert_select ".facts-strip > div:nth-child(3)", /RESOURCES\s+2 CPUs · 2 GB/
    assert_select ".facts-strip > div:nth-child(4)", /SERVICES · 2/
    assert_select ".facts-strip .service", 2
    assert_select ".facts-strip .service", /cache\s+redis:7/
    assert_select ".facts-strip > div:nth-child(5)", /REPO\s+not linked/

    fresh = make_linked_project("garage")
    get project_path(fresh.name)
    assert_select ".facts-strip > div:nth-child(1)", /never deployed/
    assert_select ".facts-strip > div:nth-child(3)", /no limits/
    assert_select ".facts-strip > div:nth-child(4)", /none besides the app/
    assert_select ".facts-strip > div:nth-child(5)", %r{forgejo · houston/garage}
  end

  # Your direction: one column of sections under the header, with a menu on
  # the left that jumps to them (docs/plans/mission-control-match-design.md, Batch 4).
  test "one column of sections under the header, with a menu" do
    make_linked_project("garage")
    get project_path("garage")
    assert_select ".project > .project-head + .facts-strip + .project-layout"
    links = css_select(".project-layout > nav.section-nav a").map { |a| [ a.text.strip, a["href"] ] }
    ids = css_select(".project-sections > section").map { |s| s["id"] }
    # Alphabetical, to scan ("order navigation and sections alphabetically").
    assert_equal %w[backup-plan webhook history maintenance secrets snapshots], ids
    assert_equal ids.map { |id| "##{id}" }, links.map(&:last)
    assert_equal [ "Backup plan", "Connect pushes", "Deploy history", "Maintenance page", "Secrets", "Snapshots" ], links.map(&:first)
    assert_select "section#snapshots > .panel__head form[action='/projects/garage/backups'] button", "Create Snapshot"
    assert_select "section#snapshots > turbo-frame#snapshots-list[loading=lazy]"
    assert_select "section#snapshots > .panel__head", /unas-nfs/i
    assert_select "section#backup-plan", /Edit in compose\.yml/

    # Only the sections the page has: an unlinked project has no Connect pushes.
    get project_path("equip")
    assert_select ".section-nav a[href='#webhook']", 0
    assert_select "section#webhook", 0
  end

  test "secrets: HOLD only for a blank required secret; optional ones can be set" do
    get project_path("equip")
    assert_select ".secrets .notice--nogo", /RAILS_MASTER_KEY and STRIPE_KEY have a value/
    assert_select ".secrets [data-secret=SENTRY_DSN] input[type=password]"
    assert_select ".secrets [data-secret=SENTRY_DSN]", /optional/

    %w[RAILS_MASTER_KEY STRIPE_KEY].each { |key| @project.secrets.create!(key:, value: "v") }
    get project_path("equip")
    assert_select ".secrets .notice--nogo", 0
    assert_select ".secrets [data-secret=RAILS_MASTER_KEY]", /set/
  end

  test "secrets are write-only" do
    @project.secrets.create!(key: "RAILS_MASTER_KEY", value: "super-secret-value-xyz")

    get project_path("equip")

    assert_not_includes response.body, "super-secret-value-xyz"
    assert_select "[data-secret=RAILS_MASTER_KEY]", /set/
    assert_select "[data-secret=RAILS_MASTER_KEY] input[name=value]", 0
    assert_select "[data-secret=RAILS_MASTER_KEY] a[href=?]", "/projects/equip?replace=RAILS_MASTER_KEY#secret-RAILS_MASTER_KEY", text: "Replace"
    assert_select "[data-secret=STRIPE_KEY] input[name=value]"
    assert_select "[data-secret=SENTRY_DSN] input[name=value]"
    assert_select ".secrets .notice--nogo", /STRIPE_KEY/
    assert_select ".secrets .notice--nogo", { text: /SENTRY_DSN/, count: 0 }

    get project_path("equip", replace: "RAILS_MASTER_KEY")
    assert_select "[data-secret=RAILS_MASTER_KEY] input[name=value]", 1 # Replace, empty
    assert_select "[data-secret=RAILS_MASTER_KEY] input[name=value][value]", 0
    assert_not_includes response.body, "super-secret-value-xyz"
  end

  test "saving a secret" do
    put project_secret_path("equip", "STRIPE_KEY"), params: { value: "sk_live_123" }
    assert_redirected_to project_path("equip")
    assert_equal "sk_live_123", @project.secrets.find_by!(key: "STRIPE_KEY").value

    put project_secret_path("equip", "STRIPE_KEY"), params: { value: "sk_live_456" }
    assert_equal "sk_live_456", @project.secrets.find_by!(key: "STRIPE_KEY").reload.value

    put project_secret_path("equip", "SENTRY_DSN"), params: { value: 'C:\\data' }
    assert_response :unprocessable_entity
    assert_select ".field__error", /backslash/
    assert_nil @project.secrets.find_by(key: "SENTRY_DSN")

    put project_secret_path("equip", "NOT_IN_THE_FILE"), params: { value: "x" }
    assert_response :not_found
    assert_nil @project.secrets.find_by(key: "NOT_IN_THE_FILE")

    sign_out
    put project_secret_path("equip", "SENTRY_DSN"), params: { value: "https://sentry" }
    assert_redirected_to new_session_path
    assert_nil @project.secrets.find_by(key: "SENTRY_DSN")
  end

  test "generate and remove" do
    post generate_project_secret_path("equip", "RAILS_MASTER_KEY")
    assert_redirected_to project_path("equip")
    value = @project.secrets.find_by!(key: "RAILS_MASTER_KEY").value
    # Long enough for any framework's key (Phoenix and Rails want 64 bytes).
    assert_operator value.bytesize, :>=, 64
    assert_match(/\A[A-Za-z0-9_-]+\z/, value)
    follow_redirect!
    assert_not_includes response.body, value

    delete project_secret_path("equip", "RAILS_MASTER_KEY")
    assert_redirected_to project_path("equip")
    assert_nil @project.secrets.find_by(key: "RAILS_MASTER_KEY")
  end

  test "connect pushes" do
    linked = make_linked_project("garage")

    get project_path("garage")
    assert_select ".webhook", /https:\/\/hooks\.svnmns\.com\/garage/
    assert_select ".webhook", /#{WEBHOOK_SECRET}/
    assert_select ".webhook", /application\/json/

    linked.update!(webhook_verified_at: Time.current)
    get project_path("garage")
    assert_not_includes response.body, WEBHOOK_SECRET
    assert_select ".webhook", /Rotate secret/

    post rotate_webhook_project_path("garage")
    assert_redirected_to project_path("garage")
    linked.reload
    assert_not_equal WEBHOOK_SECRET, linked.webhook_secret
    assert_nil linked.webhook_verified_at
    follow_redirect!
    assert_select ".webhook", /#{linked.webhook_secret}/

    ls = FakeGit.new { |args, _| args[1] == "ls-remote" ? git_ok("#{"a" * 40}\trefs/heads/main\n") : git_ok }
    use_fake_git(ls) { post check_project_path("garage") }
    assert_redirected_to project_path("garage")
    assert_equal "queued", linked.deploys.sole.status

    get project_path("equip")
    assert_select ".webhook", 0
  end

  test "domain states" do
    @project.update!(domain_states: { "equipping.com" => { "state" => "DNS PENDING", "reason" => nil } })
    get project_path("equip")
    assert_select ".host-chips", /equipping\.com\s*DNS PENDING/
  end
end
