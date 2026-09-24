require "test_helper"

class Settings::TokensControllerTest < ActionDispatch::IntegrationTest
  setup { sign_in_as users(:one) }

  test "a token is shown once" do
    post settings_tokens_path, params: { name: "laptop" }
    assert_response :success
    token = css_select(".new-token").text[/hou_[A-Za-z0-9_-]+/]
    assert token, "the page shows the new token"
    assert_operator token.length, :>=, 47
    record = ApiToken.find_by!(name: "laptop")
    assert_equal OpenSSL::Digest::SHA256.hexdigest(token), record.token_digest
    assert_not_includes ApiToken.connection.select_values("SELECT token_digest FROM api_tokens").join, token

    get settings_tokens_path
    assert_select "[data-token=laptop]", /laptop/
    assert_not_includes response.body, token

    [ "", "laptop", "x" * 51 ].each do |name|
      post settings_tokens_path, params: { name: }
      assert_response :unprocessable_entity, name.inspect
    end
    assert_equal 1, ApiToken.count
  end

  # Create answers with the page itself (200), which Turbo drops after a form
  # post, so a browser never showed the new token. It submits as plain HTML.
  test "the create form submits without Turbo, so the token shows" do
    get settings_tokens_path
    assert_select "form[action='#{settings_tokens_path}'][data-turbo='false']"
  end

  test "revoking a token" do
    post settings_tokens_path, params: { name: "agent" }
    token = css_select(".new-token").text[/hou_[A-Za-z0-9_-]+/]
    get "/api/v1/me", headers: { "Authorization" => "Bearer #{token}" }
    assert_response :success

    delete settings_token_path(ApiToken.find_by!(name: "agent"))
    assert_redirected_to settings_tokens_path
    assert_equal 0, ApiToken.count
    get "/api/v1/me", headers: { "Authorization" => "Bearer #{token}" }
    assert_response :unauthorized
  end

  test "tokens need the admin" do
    sign_out
    get settings_tokens_path
    assert_redirected_to new_session_path
    post settings_tokens_path, params: { name: "sneaky" }
    assert_redirected_to new_session_path
    assert_equal 0, ApiToken.count
  end
end
