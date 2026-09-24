require "test_helper"

# The browser tab shows Houston's logo, not Rails' stock red circle.
class FaviconTest < ActionDispatch::IntegrationTest
  test "the icons are Houston's logo" do
    get "/icon.svg"
    assert_response :success
    assert_includes response.body, %(fill="#C8321F")
    assert_includes response.body, %(stroke-dasharray="5 7")
    assert_not_includes response.body, %(fill="red")

    logo = Rails.root.join("public/icon.png").binread
    assert_equal "\x89PNG".b, logo[0, 4]
    assert_equal [ 512, 512 ], logo[16, 8].unpack("NN")
  end
end
