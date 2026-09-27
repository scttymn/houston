require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_git"

# Changing a project's repo URL from its page and the API (RepoMove).
class ProjectReposTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeGitHelper

  NEW_URL = "git@github.com:scttymn/equip.git"

  setup { @project = make_linked_project("equip") }

  def repo(name: "equip")
    FakeGit.new do |args|
      if args.include?("ls-remote") then git_ok("#{"a" * 40}\trefs/heads/main\n")
      elsif args.include?("rev-parse") then git_ok("#{"a" * 40}\n")
      elsif args.include?("inspect") then git_ok({ "sync" => { "name" => name } }.to_json)
      end
    end
  end

  test "from the project page" do
    sign_in_as users(:one)
    get project_path("equip")
    assert_select "section#webhook form[action=?] input[name=repo_url][value=?]", project_repo_path("equip"), "git@forgejo:houston/equip.git"

    use_fake_git(repo) { patch project_repo_path("equip"), params: { repo_url: NEW_URL } }
    assert_redirected_to project_path("equip", anchor: "webhook")
    follow_redirect!
    assert_select ".notice--go", /equip's repo is now git@github\.com:scttymn\/equip\.git/
    assert_equal NEW_URL, @project.reload.repo_url

    use_fake_git(repo(name: "shop")) { patch project_repo_path("equip"), params: { repo_url: "git@github.com:scttymn/shop.git" } }
    follow_redirect!
    assert_select ".notice--nogo", /names shop, not equip/
  end

  test "over the API" do
    token, = ApiToken.issue!("agent")
    headers = { "Authorization" => "Bearer #{token}", "Content-Type" => "application/json" }
    use_fake_git(repo) { put "/api/v1/projects/equip/repo", params: { repo_url: NEW_URL }.to_json, headers: }
    assert_response :success
    assert_equal({ "repo_url" => NEW_URL }, response.parsed_body)

    put "/api/v1/projects/equip/repo", params: { repo_url: NEW_URL }.to_json, headers: headers
    assert_response :unprocessable_entity
    assert_match "already", response.parsed_body["error"]

    put "/api/v1/projects/equip/repo", params: { repo_url: NEW_URL }.to_json, headers: { "Content-Type" => "application/json" }
    assert_response :unauthorized
  end

  test "signed out" do
    patch project_repo_path("equip"), params: { repo_url: NEW_URL }
    assert_redirected_to sign_in_path
    assert_equal "git@forgejo:houston/equip.git", @project.reload.repo_url
  end
end
