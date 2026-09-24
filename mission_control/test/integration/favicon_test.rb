require "test_helper"

# The browser tab shows Houston's logo, not Rails' stock red circle, from
# fingerprinted URLs: Cloudflare caches the icons for a year, so a changed
# icon needs a new URL to reach anyone.
class FaviconTest < ActionDispatch::IntegrationTest
  test "the icons are Houston's logo, at fingerprinted URLs" do
    get new_session_path
    svg, png = %w[image/svg+xml image/png].map { |type| css_select("link[rel=icon][type='#{type}']").first&.[]("href") }
    assert_match %r{\A/assets/icon-\h+\.svg\z}, svg.to_s
    assert_match %r{\A/assets/icon-\h+\.png\z}, png.to_s

    get svg
    assert_response :success
    assert_includes response.body, %(fill="#C8321F")
    assert_not_includes response.body, %(fill="red")

    get png
    assert_response :success
    assert_equal [ 512, 512 ], response.body.b[16, 8].unpack("NN")
  end
end
