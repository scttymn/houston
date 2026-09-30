# An app's CPU, memory and disk on the flight board (AppStats), as tick
# gauges: a label, ten ticks lit up to the share used, the percentage, and the
# amount used. A share is of the limit, or, without one, of the host's (its
# cores, its memory, its disk): the hover says which. Only when neither is
# known does no tick light and the percentage read as a dash.
module UsageHelper
  NEAR = 85 # percent: from here the ticks and the amount turn amber

  # 0.15 → "0.15 cores", 1 → "1 core"; under a hundredth, three places
  # (0.003 cores), and "<0.001 cores" below that, so a running app never
  # reads as none.
  def cores_words(cores)
    n = format(cores.positive? && cores < 0.01 ? "%.3f" : "%.2f", cores).sub(/\.?0+\z/, "")
    return "<0.001 cores" if n == "0" && cores.positive?
    "#{n} #{n == "1" ? "core" : "cores"}"
  end

  # A size as the board writes it: number_to_human_size, with bytes as "B"
  # ("0 B", as the CLI writes it), not "0 Bytes".
  def size_words(bytes) = number_to_human_size(bytes).sub(/ Bytes?\z/, " B")

  # One figure: kind (cpu, memory or disk), its amount in words, and what
  # it's a share of in words (the limit, or the host's when of_host), for
  # the hover, with the percentage used.
  def usage(kind, label, amount, limit: nil, percent: nil, of_host: false)
    near = percent && percent >= NEAR
    title = if limit.nil? then "#{label} #{amount} · no limit set"
    elsif of_host then "#{label} #{amount} of the host's #{limit} (#{percent_words(percent)}) · no limit set"
    else "#{label} #{amount} of #{limit} (#{percent_words(percent)})"
    end
    tag.span(class: [ "usage", "usage--#{kind}", ("usage--near" if near) ], title:) do
      safe_join([
        tag.span(label, class: "mono usage__label"),
        gauge(percent),
        tag.span(percent_words(percent), class: "mono usage__percent"),
        tag.span(amount, class: "usage__amount")
      ])
    end
  end

  # 42.4 → "42%", 0.04 → "<1%", 0 → "0%", nil → "—".
  def percent_words(percent)
    return "—" if percent.nil?
    return "<1%" if percent.positive? && percent < 0.5
    "#{percent.round}%"
  end

  # Ten ticks, one lit per 10% of the whole (rounded), and at least one for
  # any use, so 2% doesn't read as 0%. With nothing to measure against, none
  # (gauge--open).
  def gauge(percent)
    on = percent&.positive? ? (percent / 10.0).round.clamp(1, 10) : 0
    tag.span(class: [ "gauge", ("gauge--open" unless percent) ], role: "img", "aria-label": percent ? percent_words(percent) : "no limit") do
      safe_join(Array.new(10) { |i| tag.span(class: [ "gauge__tick", ("is-on" if i < on) ]) })
    end
  end
end
