# Requests to Mission Control's local API, as houston deploy makes them.
module ApiHelpers
  RUNNER_TOKEN = "runner-#{"a1b2c3d4" * 6}"

  def self.included(base)
    base.setup { ENV["HOUSTON_RUNNER_TOKEN"] = RUNNER_TOKEN }
    base.teardown { ENV.delete("HOUSTON_RUNNER_TOKEN") }
  end

  def api_headers(token: RUNNER_TOKEN, **extra)
    { "Authorization" => "Bearer #{token}", "Content-Type" => "application/json" }.merge(extra)
  end

  def equip_payload(**changes)
    {
      name: "equip",
      app_service: "app",
      services: %w[app db],
      domains: [],
      variables: [ { name: "RAILS_MASTER_KEY", required: true }, { name: "SENTRY_DSN", required: false } ],
      health: "/up",
      port: 80,
      deploy_rule: { on: "commit", branch: "main", tags: "v*" }
    }.merge(changes)
  end

  def sync(payload = equip_payload, headers: api_headers)
    post "/api/projects/sync", params: payload.is_a?(String) ? payload : payload.to_json, headers:
  end

  def json = response.parsed_body
end
