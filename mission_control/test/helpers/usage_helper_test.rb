require "test_helper"

# The flight board's tick gauges (docs/plans/app-stats.md): one tick per 10%
# of the limit, amber at 85% and up.
class UsageHelperTest < ActionView::TestCase
  def ticks_on(html) = Nokogiri::HTML.fragment(html).css(".gauge__tick.is-on").size

  test "one tick per 10% of the limit" do
    { 0 => 0, 4 => 0, 5 => 1, 16 => 2, 42 => 4, 69 => 7, 95 => 10, 100 => 10, 140 => 10 }.each do |percent, on|
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
    assert_equal "1.9 GB / 2 GB", near.at_css(".usage__amount").text
    assert_nil Nokogiri::HTML.fragment(usage(:memory, "MEM", "1.7 GB", limit: "2 GB", percent: 84)).at_css(".usage--near")
    assert_nil Nokogiri::HTML.fragment(usage(:disk, "DISK", "3.8 GB")).at_css(".usage--near")
  end

  test "sizes in words" do
    assert_equal [ "0 B", "1 B", "500 B", "20 MB", "1.5 GB" ], [ 0, 1, 500, 20 * 1024**2, 1.5 * 1024**3 ].map { size_words(it) }
  end

  test "cores in words" do
    assert_equal [ "0 cores", "0.15 cores", "1 core", "2.5 cores" ], [ 0, 0.15, 1, 2.5 ].map { cores_words(it) }
  end
end
