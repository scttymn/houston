require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_git"

class ApiV1ActionsTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeGitHelper

  A = "a" * 40
  B = "b" * 40

  setup { @token, = ApiToken.issue!("agent") }

  def api(verb, path, body = nil)
    send(verb, "/api/v1#{path}", params: body&.to_json, headers: { "Authorization" => "Bearer #{@token}", "Content-Type" => "application/json" })
  end

  def json = response.parsed_body
  def refs(map) = FakeGit.new { |args, _| args[1] == "ls-remote" ? git_ok(map.map { |ref, sha| "#{sha}\t#{ref}\n" }.join) : git_ok }

  test "deploy queues the head" do
    garage = make_linked_project("garage")
    garage.update!(seen_refs: { "refs/heads/main" => A })
    use_fake_git(refs("refs/heads/main" => A)) { api :post, "/projects/garage/deploys" }
    assert_response :success
    assert_equal [ 1, A, "refs/heads/main", "queued" ], json.values_at("number", "sha", "ref", "status")

    use_fake_git(refs("refs/heads/main" => B)) { api :post, "/projects/garage/deploys" }
    assert_equal [ 1, B ], json.values_at("number", "sha"), "a queued deploy switches to the newest head"

    tagged = make_linked_project("tagged", deploy_rule: { "on" => "tag", "tags" => "v*" })
    use_fake_git(refs("refs/tags/v1.9" => A, "refs/tags/v1.10" => B, "refs/tags/beta" => A)) { api :post, "/projects/tagged/deploys" }
    assert_equal [ "refs/tags/v1.10", B ], json.values_at("ref", "sha")
    assert tagged

    make_project("unlinked")
    api :post, "/projects/unlinked/deploys"
    assert_response :unprocessable_entity
    assert_match(/link the repo first/, json["error"])

    use_fake_git(FakeGit.new { git_failure("fatal: Could not read from remote repository.\n") }) { api :post, "/projects/garage/deploys" }
    assert_response :bad_gateway
    assert_match(/Could not read/, json["error"])
  end

  test "linking over the API" do
    ls = "4be21c0aa11b2c3d4e5f60718293a4b5c6d7e8f9\trefs/heads/main\n"
    responder = lambda do |args, _|
      case args
      in [ "git", "ls-remote", * ] then git_ok(ls)
      in [ "git", "-C", _, "rev-parse", "HEAD" ] then git_ok("4be21c0aa11b2c3d4e5f60718293a4b5c6d7e8f9\n")
      in [ _, "-f", _, "inspect", "--json" ] then git_ok(garage_inspection.to_json)
      else git_ok
      end
    end
    use_fake_git(FakeGit.new(&responder)) do |git|
      api :post, "/links", { repo_url: "-oProxyCommand=id" }
      assert_response :unprocessable_entity
      assert_empty git.calls

      api :post, "/links", { repo_url: "git@forgejo:houston/garage.git" }
      assert_response :success
      id = json["id"]
      assert_match(/\Assh-ed25519 /, json["deploy_key"])
      assert_equal true, json.dig("access", "ok")

      api :post, "/links/#{id}/save"
      assert_response :unprocessable_entity
      assert_match(/read the file first/i, json["error"])

      api :post, "/links/#{id}/access"
      assert_equal true, json["ok"]
      api :post, "/links/#{id}/read", { branch: "main", compose_path: "compose.yml" }
      assert_equal [ true, "garage", "4be21c0" ], [ json["ok"], json.dig("found", "name"), json.dig("found", "sha").first(7) ]

      api :post, "/links/#{id}/save"
      assert_response :success
      project = Project.find_by!(name: "garage")
      assert_equal [ "garage", "https://hooks.svnmns.com/garage", project.webhook_secret ], json.values_at("project", "webhook_url", "webhook_secret")
      assert_equal "git@forgejo:houston/garage.git", project.repo_url
    end
  end

  test "the webhook" do
    garage = make_linked_project("garage")
    api :get, "/projects/garage/webhook"
    assert_equal [ "https://hooks.svnmns.com/garage", false, WEBHOOK_SECRET ], json.values_at("url", "verified", "secret")

    garage.update!(webhook_verified_at: Time.current)
    api :get, "/projects/garage/webhook"
    assert_equal [ true, nil ], json.values_at("verified", "secret")
    assert_not_includes response.body, WEBHOOK_SECRET

    api :post, "/projects/garage/webhook/rotate"
    assert_response :success
    assert_equal garage.reload.webhook_secret, json["secret"]
    assert_not_equal WEBHOOK_SECRET, json["secret"]
    assert_equal false, json["verified"]
  end
end
