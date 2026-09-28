# Mission Control's error pages, public/*.html, which Rails serves when a
# request fails. They load nothing (Mission Control may be what's broken),
# so the patch is inlined. `bin/rails error_pages` writes them; run it after
# changing this file or the patch.
module ErrorPages
  Page = Data.define(:code, :heading, :text, :back)

  PAGES = {
    "400.html" => Page.new("ERROR 400 · BAD REQUEST", "Houston, we have a garbled transmission...",
      "The request didn't make sense. Check the address and try again.", true),
    "404.html" => Page.new("ERROR 404 · NOT FOUND", "Houston, we have a problem...",
      "The page you were looking for isn't here. The address may be mistyped, or the page may have moved.", true),
    "406-unsupported-browser.html" => Page.new("ERROR 406 · BROWSER NOT SUPPORTED", "Houston, we're out of range...",
      "Mission Control needs a recent browser: an up-to-date Safari, Chrome, Firefox or Edge.", false),
    "422.html" => Page.new("ERROR 422 · REQUEST REJECTED", "Abort, abort...",
      "That form was out of date. Go back, reload the page and try again.", true),
    "500.html" => Page.new("ERROR 500 · SOMETHING BROKE", "Houston, we have a problem...",
      "Mission Control hit an error. Try again, and if it keeps happening, check its logs.", true)
  }.freeze

  def self.html(file)
    page = PAGES.fetch(file)
    <<~HTML.gsub(/^\n/, "")
      <!doctype html>
      <html lang="en">
      <head>
      <meta charset="utf-8">
      <meta name="viewport" content="width=device-width, initial-scale=1">
      <meta name="robots" content="noindex, nofollow">
      <title>#{page.heading.delete_suffix("...")}</title>
      <!-- Written by bin/rails error_pages (lib/error_pages.rb); edit that, not this. -->
      <style>
        * { box-sizing: border-box; }
        body { margin: 0; min-height: 100vh; min-height: 100dvh; display: grid; place-items: center; padding: 32px 16px;
               background: #F3EFE4; color: #1A1D24; font: 16px/1.5 -apple-system, "Segoe UI", Helvetica, Arial, sans-serif; }
        main { width: 100%; max-width: 34rem; text-align: center; }
        .patch { display: block; width: 240px; max-width: 64vw; height: auto; margin: 0 auto 32px; }
        .code { margin: 0 0 8px; color: #C8321F; font: 500 13px ui-monospace, "SF Mono", Menlo, Consolas, monospace; letter-spacing: 0.12em; }
        h1 { margin: 0; font-family: "Barlow Condensed", "Arial Narrow", "Helvetica Neue", sans-serif; font-weight: 700;
             font-size: clamp(30px, 8vw, 44px); line-height: 1.1; letter-spacing: 0.02em; text-transform: uppercase; }
        p { margin: 12px 0 0; color: #4A4F5A; }
        .button { display: inline-flex; align-items: center; height: 48px; margin-top: 28px; padding: 0 24px;
                  border: 1px solid #1A1D24; background: #1A1D24; color: #F3EFE4; font-weight: 600; font-size: 15px; text-decoration: none; }
        .button:hover, .button:focus-visible { background: #262A33; }
      </style>
      </head>
      <body>
      <main>
      #{patch}
        <p class="code">#{page.code}</p>
        <h1>#{page.heading}</h1>
        <p>#{page.text}</p>
      #{'  <a class="button" href="/">Back to the flight board</a>' if page.back}
      </main>
      </body>
      </html>
    HTML
  end

  def self.write
    PAGES.each_key { |file| Rails.public_path.join(file).write(html(file)) }
  end

  def self.patch
    Rails.root.join("app/assets/images/patch.svg").read
      .sub(/<!--.*?-->\n?/m, "").strip
      .sub("<svg ", %(<svg role="img" aria-label="Houston" class="patch" ))
  end
  private_class_method :patch
end
