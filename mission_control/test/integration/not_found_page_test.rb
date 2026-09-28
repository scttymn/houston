require "test_helper"

# Mission Control's 404 is public/404.html: the patch, and the problem.
class NotFoundPageTest < ActionDispatch::IntegrationTest
  test "an unknown path gets Houston's 404: the patch and the problem" do
    # As in production: the public page, not the debugging one.
    Rails.application.env_config["action_dispatch.show_detailed_exceptions"] = false
    get "/no-such-page"
    assert_response :not_found
    assert_select "title", "Houston, we have a problem"
    assert_select "h1", "Houston, we have a problem..."
    assert_select "main svg.patch[role=img][aria-label='Houston']", 1
    assert_equal 7, response.body.scan(%(fill="#F3EFE4"/>)).size, "seven stars, for Seven Moons"
    assert_select "a[href='/']", "Back to the flight board"
  ensure
    Rails.application.env_config.delete("action_dispatch.show_detailed_exceptions")
  end

  test "the page inlines the patch as it is: it loads nothing, so a new logo needs a new page" do
    patch = Rails.root.join("app/assets/images/patch.svg").read.sub(/<!--.*?-->\n?/m, "").strip
    page = Rails.root.join("public/404.html").read
    assert_includes page, patch.sub("<svg ", %(<svg role="img" aria-label="Houston" class="patch" ))
    assert_no_match %r{<(?:link|script|img)\b}, page
  end
end
