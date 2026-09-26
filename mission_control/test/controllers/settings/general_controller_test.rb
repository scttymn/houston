require "test_helper"

class Settings::GeneralControllerTest < ActionDispatch::IntegrationTest
  # The design's Settings: one page, a menu on the left that jumps to its
  # sections, and the sections in one column.
  test "settings is one page: the menu and its sections" do
    Installation.current.update!(dns_mode: "per_host")
    sign_in_as users(:one)
    get settings_path
    assert_select ".settings > nav.section-nav h1", /Settings/i
    links = css_select("nav.section-nav a").map { |a| [ a.text.strip, a["href"] ] }
    assert_equal [ [ "API tokens", "#tokens" ], [ "Cloudflare", "#cloudflare" ], [ "Houston", "#houston" ], [ "Port 3000", "#port" ], [ "Storage", "#storage" ], [ "Time zone", "#time-zone" ] ], links
    assert_equal %w[tokens cloudflare houston port storage time-zone], css_select(".settings__body > section").map { |s| s["id"] }
    assert_select "header a[href='#{settings_path}']", "Settings"

    { settings_general_path => "cloudflare", settings_storage_locations_path => "storage", settings_tokens_path => "tokens" }.each do |old, section|
      get old
      assert_redirected_to settings_path(anchor: section)
    end

    get settings_path
    assert_select ".settings__body section#cloudflare", /BASE DOMAIN\s+svnmns\.com/
    assert_select "section#cloudflare", /DNS\s+Host by host/
    assert_select "section#cloudflare turbo-frame#cloudflare-live", 0, "no live panel before there's a tunnel (with one: settings/cloudflare_controller_test.rb)"
    assert_select "section#time-zone .inline-form select[name=time_zone]"
  end

  test "the time zone" do
    sign_in_as users(:one)
    get settings_path
    assert_response :success
    assert_select "select[name='time_zone'] option[selected]", "UTC"

    patch settings_general_path, params: { time_zone: "Europe/Berlin" }
    assert_redirected_to settings_path(anchor: "time-zone")
    assert_equal "Europe/Berlin", Installation.current.time_zone
    follow_redirect!
    assert_select "select[name='time_zone'] option[selected]", "Europe/Berlin"

    patch settings_general_path, params: { time_zone: "Mars/Olympus" }
    assert_response :unprocessable_entity
    assert_equal "Europe/Berlin", Installation.current.reload.time_zone

    get root_path
    assert_select "header a[href='#{settings_path}']", "Settings"
  end

  test "settings need the admin" do
    patch settings_general_path, params: { time_zone: "Europe/Berlin" }
    assert_redirected_to sign_in_path
    assert_equal "UTC", Installation.current.time_zone
  end
end
