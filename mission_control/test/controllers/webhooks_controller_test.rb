require "test_helper"
require_relative "../support/project_helpers"

class WebhooksControllerTest < ActionDispatch::IntegrationTest
  include ActiveJob::TestHelper
  include ProjectHelpers

  BODY = { ref: "refs/heads/main", after: "4be21c0" }.to_json

  setup do
    host! "hooks.svnmns.com"
    @project = make_linked_project("garage")
  end

  def hmac(body = BODY, secret = WEBHOOK_SECRET) = OpenSSL::HMAC.hexdigest("SHA256", secret, body)
  def ring(name: "garage", body: BODY, headers: {}) = post("/#{name}", params: body, headers: { "Content-Type" => "application/json" }.merge(headers))

  test "only a signed push rings the doorbell" do
    {
      "GitHub" => { "X-Hub-Signature-256" => "sha256=#{hmac}", "X-GitHub-Event" => "push" },
      "Forgejo" => { "X-Forgejo-Signature" => hmac },
      "Gitea" => { "X-Gitea-Signature" => hmac },
      "GitLab" => { "X-Gitlab-Token" => WEBHOOK_SECRET },
      "Houston" => { "X-Houston-Token" => WEBHOOK_SECRET }
    }.each do |host, headers|
      assert_enqueued_with(job: CheckForChangesJob, args: [ @project.id ]) { ring(headers:) }
      assert_response :accepted, host
      assert_empty response.body, host
    end

    make_project("unlinked")
    {
      "wrong HMAC" => [ "garage", BODY, { "X-Hub-Signature-256" => "sha256=#{hmac(BODY, "nope")}" } ],
      "HMAC of another body" => [ "garage", BODY, { "X-Forgejo-Signature" => hmac("{}") } ],
      "no signature" => [ "garage", BODY, {} ],
      "wrong token" => [ "garage", BODY, { "X-Gitlab-Token" => "nope" } ],
      "unknown project" => [ "nope", BODY, { "X-Hub-Signature-256" => "sha256=#{hmac}" } ],
      "unlinked project" => [ "unlinked", BODY, { "X-Houston-Token" => "" } ]
    }.each do |what, (name, body, headers)|
      assert_no_enqueued_jobs { ring(name:, body:, headers:) }
      assert_response :not_found, what
      assert_empty response.body, what
    end
  end

  test "the first delivery is remembered" do
    assert_nil @project.webhook_verified_at
    travel_to(Time.utc(2026, 9, 23, 12)) { ring(headers: { "X-Forgejo-Signature" => hmac }) }
    first = @project.reload.webhook_verified_at
    assert_equal Time.utc(2026, 9, 23, 12), first

    travel_to(Time.utc(2026, 9, 24, 12)) { ring(headers: { "X-Forgejo-Signature" => hmac }) }
    assert_equal first, @project.reload.webhook_verified_at
  end

  test "webhooks are bounded" do
    big = "x" * (5.megabytes + 1)
    assert_no_enqueued_jobs { ring(body: big, headers: { "X-Forgejo-Signature" => hmac(big) }) }
    assert_response :content_too_large

    Rails.cache.clear # the oversized request counted toward the limit too
    30.times { ring(headers: { "X-Houston-Token" => WEBHOOK_SECRET }) }
    assert_response :accepted
    ring(headers: { "X-Houston-Token" => WEBHOOK_SECRET })
    assert_response :too_many_requests
  end
end
