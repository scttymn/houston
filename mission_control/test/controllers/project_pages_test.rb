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
    assert_select ".facts", /Every commit to main/
    assert_select ".facts", /db.*cache|cache.*db/m
    assert_select ".facts", /#{format("%040x", 12)[0, 7]}/
    assert_select ".history [data-deploy]", 10
    assert_select ".history [data-deploy]:first-child", /#12/
    assert_select ".history a[href='/projects/equip/deploys/12']"

    get project_path("equip", page: 2)
    assert_select ".history [data-deploy]", 2
    assert_select ".history", /#2/
    assert_select ".history", /#1/

    get project_path("nope")
    assert_response :not_found
  end

  test "secrets are write-only" do
    @project.secrets.create!(key: "RAILS_MASTER_KEY", value: "super-secret-value-xyz")

    get project_path("equip")

    assert_not_includes response.body, "super-secret-value-xyz"
    assert_select "[data-secret=RAILS_MASTER_KEY]", /set/
    assert_select "[data-secret=RAILS_MASTER_KEY] input[name=value]", 1 # inside Replace, empty
    assert_select "[data-secret=RAILS_MASTER_KEY] input[name=value][value]", 0
    assert_select "[data-secret=STRIPE_KEY] input[name=value]"
    assert_select "[data-secret=SENTRY_DSN] input[name=value]"
    assert_select ".secrets .notice--hold", /STRIPE_KEY/
    assert_select ".secrets .notice--hold", { text: /SENTRY_DSN/, count: 0 }
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
    assert_operator value.length, :>=, 43
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
end
