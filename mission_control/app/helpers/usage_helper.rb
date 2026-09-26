# An app's CPU, memory and disk on the flight board (AppStats), as the
# design's tick gauges: a label, ten ticks (one per 10% of the limit), the
# percentage, and the amount against the limit. Without a limit the ticks
# are outlined, which keeps the column lined up, and only the amount shows.
module UsageHelper
  NEAR = 85 # percent: from here the ticks and the amount turn amber

  # 0.15 → "0.15 cores", 1 → "1 core".
  def cores_words(cores)
    n = format("%.2f", cores).sub(/\.?0+\z/, "")
    "#{n} #{n == "1" ? "core" : "cores"}"
  end

  # A size as the board writes it: number_to_human_size, with bytes as "B"
  # ("0 B", as the CLI writes it), not "0 Bytes".
  def size_words(bytes) = number_to_human_size(bytes).sub(/ Bytes?\z/, " B")

  # One figure: kind (cpu, memory or disk), its amount in words, and, with a
  # limit, the limit in words and the percentage used.
  def usage(kind, label, amount, limit: nil, percent: nil)
    near = percent && percent >= NEAR
    title = limit ? "#{label} #{amount} of #{limit} (#{percent}%)" : "#{label} #{amount} · no limit set"
    tag.span(class: [ "usage", "usage--#{kind}", ("usage--near" if near) ], title:) do
      safe_join([
        tag.span(label, class: "mono usage__label"),
        gauge(percent),
        tag.span(percent ? "#{percent}%" : "—", class: "mono usage__percent"),
        tag.span(class: "usage__amount") { safe_join([ amount, (tag.span(" / #{limit}", class: "usage__limit") if limit) ].compact) }
      ])
    end
  end

  # Ten ticks, one lit per 10% of the limit (rounded). Without a limit, the
  # ticks are outlined (gauge--open).
  def gauge(percent)
    on = percent ? (percent / 10.0).round.clamp(0, 10) : 0
    tag.span(class: [ "gauge", ("gauge--open" unless percent) ], role: "img", "aria-label": percent ? "#{percent}%" : "no limit") do
      safe_join(Array.new(10) { |i| tag.span(class: [ "gauge__tick", ("is-on" if i < on) ]) })
    end
  end
end
