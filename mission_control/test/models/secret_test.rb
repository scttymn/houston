require "test_helper"

class SecretTest < ActiveSupport::TestCase
  setup do
    @project = Project.create!(name: "equip", app_service: "app", services: %w[app], health: "/up", port: 80)
  end

  test "secrets Kamal can carry, encrypted" do
    [ "back\\slash", "line\nbreak", "tab\there", "nul\u0000" ].each do |value|
      secret = @project.secrets.build(key: "TOKEN", value:)
      assert_not secret.valid?, value.inspect
      assert_match(/base64/i, secret.errors[:value].join)
    end
    [ "my.key", "1ST", "" ].each do |key|
      assert_not @project.secrets.build(key:, value: "x").valid?, key.inspect
    end

    secret = @project.secrets.create!(key: "TOKEN", value: "$HOME 'q' \"d\" café ☕")
    assert_equal "$HOME 'q' \"d\" café ☕", secret.reload.value
    raw = Secret.connection.select_value("SELECT value FROM secrets WHERE id = #{secret.id}")
    assert_not_includes raw, "café"
  end
end
