require "test_helper"

# The flight board's tick gauges (docs/plans/app-stats.md): one tick per 10%
# of the limit, amber at 85% and up.
class UsageHelperTest < ActionView::TestCase
  def ticks_on(html) = Nokogiri::HTML.fragment(html).css(".gauge__tick.is-on").size

  # Any use lights a tick, so 2% doesn't look like 0% (docs/plans/app-stats.md, row 10).
  test "one tick per 10% of the limit, and one for any use" do
    { 0 => 0, 1 => 1, 2 => 1, 4 => 1, 5 => 1, 16 => 2, 26 => 3, 42 => 4, 69 => 7, 95 => 10, 100 => 10, 140 => 10 }.each do |percent, on|
      assert_equal on, ticks_on(gauge(percent)), "#{percent}%"
    end
    assert_equal 0, ticks_on(gauge(nil)), "no limit"
    assert_equal 10, Nokogiri::HTML.fragment(gauge(nil)).css(".gauge.gauge--open .gauge__tick").size, "outlined ticks without a limit"
    assert_nil Nokogiri::HTML.fragment(gauge(42)).at_css(".gauge--open"), "filled, unlit ticks with one"
  end

  test "amber at 85% and up" do
    near = Nokogiri::HTML.fragment(usage(:memory, "MEM", "1.9 GB", limit: "2 GB", percent: 95))
    assert near.at_css(".usage.usage--near")
    assert_equal "95%", near.at_css(".usage__percent").text
    assert_equal "1.9 GB", near.at_css(".usage__amount").text, "the limit is on the project's page and in the hover"
    assert_equal "MEM 1.9 GB of 2 GB (95%)", near.at_css(".usage")["title"]
    assert_nil Nokogiri::HTML.fragment(usage(:memory, "MEM", "1.7 GB", limit: "2 GB", percent: 84)).at_css(".usage--near")
    assert_nil Nokogiri::HTML.fragment(usage(:disk, "DISK", "3.8 GB")).at_css(".usage--near")
  end

  # A share of the host's (no limit set): a sliver reads <1%, with a tick,
  # as the app is running; the hover says whose.
  test "shares of the host" do
    host = Nokogiri::HTML.fragment(usage(:memory, "MEM", "7 MB", limit: "15.6 GB", percent: 0.04, of_host: true))
    assert_equal "<1%", host.at_css(".usage__percent").text
    assert_equal 1, host.css(".gauge__tick.is-on").size
    assert_nil host.at_css(".gauge--open")
    assert_equal "MEM 7 MB of the host's 15.6 GB (<1%) · no limit set", host.at_css(".usage")["title"]
    assert_equal [ "—", "0%", "<1%", "1%", "42%" ], [ nil, 0, 0.3, 0.6, 42.4 ].map { percent_words(it) }
  end

  test "sizes in words" do
    assert_equal [ "0 B", "1 B", "500 B", "20 MB", "1.5 GB" ], [ 0, 1, 500, 20 * 1024**2, 1.5 * 1024**3 ].map { size_words(it) }
  end

  test "cores in words" do
    assert_equal [ "0 cores", "0.15 cores", "1 core", "2.5 cores" ], [ 0, 0.15, 1, 2.5 ].map { cores_words(it) }
    assert_equal [ "0.01 cores", "0.003 cores", "<0.001 cores" ], [ 0.01, 0.003, 0.0004 ].map { cores_words(it) }, "a running app never reads as none"
  end
end
