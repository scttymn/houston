require "test_helper"

class Settings::GeneralControllerTest < ActionDispatch::IntegrationTest
  test "the time zone" do
    sign_in_as users(:one)
    get settings_general_path
    assert_response :success
    assert_select "select[name='time_zone'] option[selected]", "UTC"
    assert_select "nav.settings-nav a[href='#{settings_tokens_path}']"

    patch settings_general_path, params: { time_zone: "Europe/Berlin" }
    assert_redirected_to settings_general_path
    assert_equal "Europe/Berlin", Installation.current.time_zone
    follow_redirect!
    assert_select "select[name='time_zone'] option[selected]", "Europe/Berlin"

    patch settings_general_path, params: { time_zone: "Mars/Olympus" }
    assert_response :unprocessable_entity
    assert_equal "Europe/Berlin", Installation.current.reload.time_zone

    get root_path
    assert_select "header a[href='#{settings_general_path}']", "Settings"
  end

  test "settings need the admin" do
    patch settings_general_path, params: { time_zone: "Europe/Berlin" }
    assert_redirected_to new_session_path
    assert_equal "UTC", Installation.current.time_zone
  end
end
