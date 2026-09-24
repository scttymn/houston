require "test_helper"

# Houston's logo is the lunar module: the patch on the sign-in page, the
# lander alone in the top bar and the browser tab. All from fingerprinted
# URLs: Cloudflare caches them for a year, so a changed logo needs a new URL.
class FaviconTest < ActionDispatch::IntegrationTest
  test "the favicon is the lander, following the browser's light or dark mode" do
    get sign_in_path
    svg, png = %w[image/svg+xml image/png].map { |type| css_select("link[rel=icon][type='#{type}']").first&.[]("href") }
    assert_match %r{\A/assets/icon-\h+\.svg\z}, svg.to_s
    assert_match %r{\A/assets/icon-\h+\.png\z}, png.to_s

    get svg
    assert_response :success
    assert_includes response.body, "prefers-color-scheme: dark"
    assert_includes response.body, %(stroke="#1A1D24"), "ink lines in a light tab"
    assert_includes response.body, %(stroke="#F3EFE4"), "cream lines in a dark one"
    assert_not_includes response.body, %(fill="#C8321F"), "the old orbit mark"

    get png
    assert_response :success
    assert_equal [ 512, 512 ], response.body.b[16, 8].unpack("NN")
  end

  test "the sign-in page shows the patch; the top bar shows the lander" do
    get sign_in_path
    assert_select "img.signin__patch[src^='/assets/patch-'][src$='.svg'][alt='Houston Mission Control'][width='320']", 1
    patch = css_select("img.signin__patch").first["src"]
    get patch
    assert_response :success
    assert_not_includes response.body, "<text", "the lettering is outlined: no fonts needed"
    assert_equal 7, response.body.scan(%(fill="#F3EFE4"/>)).size, "seven stars, for Seven Moons"

    sign_in_as users(:one)
    get root_path
    assert_select ".topbar .brand img.brand__mark[src^='/assets/lander-'][src$='.svg'][alt=''][width='40']", 1
    assert_select ".topbar .brand .brand__name", /HOUSTON\s*MISSION CONTROL/
  end
end
