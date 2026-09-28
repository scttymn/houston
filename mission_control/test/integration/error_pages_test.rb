require "test_helper"

# Mission Control's error pages are public/*.html, written by
# `bin/rails error_pages` from one template: the patch, a line, a way back.
class ErrorPagesTest < ActionDispatch::IntegrationTest
  PAGES = {
    "400.html" => [ "ERROR 400 · BAD REQUEST", "Houston, we have a garbled transmission..." ],
    "404.html" => [ "ERROR 404 · NOT FOUND", "Houston, we have a problem..." ],
    "406-unsupported-browser.html" => [ "ERROR 406 · BROWSER NOT SUPPORTED", "Houston, we're out of range..." ],
    "422.html" => [ "ERROR 422 · REQUEST REJECTED", "Abort, abort..." ],
    "500.html" => [ "ERROR 500 · SOMETHING BROKE", "Houston, we have a problem..." ]
  }.freeze

  test "every error page is the patch and its line, and loads nothing" do
    assert_equal PAGES.keys.sort, ErrorPages::PAGES.keys.sort
    PAGES.each do |file, (code, heading)|
      source = Rails.root.join("public", file).read
      page = Nokogiri::HTML5(source)
      assert_equal heading.delete_suffix("..."), page.at("title")&.text, file
      assert_equal code, page.at("main .code")&.text, file
      assert_equal heading, page.at("main h1")&.text, file
      assert_equal 1, page.css("main svg.patch[role=img][aria-label='Houston']").size, file
      assert_equal 7, source.scan(%(fill="#F3EFE4"/>)).size, "#{file}: seven stars, for Seven Moons"
      assert_empty page.css("link, script, img"), file
    end
  end

  test "the pages offer a way back to the flight board, except to a browser that can't use it" do
    %w[400.html 404.html 422.html 500.html].each do |file|
      page = Nokogiri::HTML5(Rails.root.join("public", file).read)
      assert_equal "Back to the flight board", page.at("main a[href='/']")&.text, file
    end
    assert_nil Nokogiri::HTML5(Rails.root.join("public/406-unsupported-browser.html").read).at("main a")
  end

  test "the pages are what bin/rails error_pages writes, so a new logo means running it" do
    ErrorPages::PAGES.each_key do |file|
      assert_equal ErrorPages.html(file), Rails.root.join("public", file).read, "#{file} is stale: run bin/rails error_pages"
    end
    assert_includes ErrorPages.html("404.html"),
      Rails.root.join("app/assets/images/patch.svg").read.sub(/<!--.*?-->\n?/m, "").strip.sub("<svg ", %(<svg role="img" aria-label="Houston" class="patch" ))
  end

  test "an unknown path gets the 404, and an old browser the 406" do
    # As in production: the public pages, not the debugging one.
    Rails.application.env_config["action_dispatch.show_detailed_exceptions"] = false
    get "/no-such-page"
    assert_response :not_found
    assert_select "h1", "Houston, we have a problem..."

    get sign_in_path, headers: { "User-Agent" => "Mozilla/5.0 (Windows NT 6.1; Trident/7.0; rv:11.0) like Gecko" }
    assert_response :not_acceptable
    assert_select "h1", "Houston, we're out of range..."
  ensure
    Rails.application.env_config.delete("action_dispatch.show_detailed_exceptions")
  end
end
