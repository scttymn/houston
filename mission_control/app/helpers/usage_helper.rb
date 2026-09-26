# An app's CPU, memory and disk on the flight board (AppStats): an amount,
# and a donut of the percentage when there's a limit.
module UsageHelper
  # 0.15 → "0.15 cores", 1 → "1 core".
  def cores_words(cores)
    n = format("%.2f", cores).sub(/\.?0+\z/, "")
    "#{n} #{n == "1" ? "core" : "cores"}"
  end

  # One figure: kind (cpu, memory or disk), its amount in words, and, with a
  # limit, the limit in words and the percentage used.
  def usage(kind, label, amount, limit: nil, percent: nil)
    title = limit ? "#{amount} of the #{limit} limit (#{percent}%)" : "#{amount}#{", no limit set" unless kind == :disk}"
    tag.span(class: "usage usage--#{kind}", title:) do
      safe_join([ (donut(percent) if percent), tag.span(label, class: "mono usage__label"), tag.span(amount, class: "usage__amount") ].compact)
    end
  end

  # A small ring filled to percent (0–100, more is shown full), coloured by
  # how close to the limit it is.
  def donut(percent)
    shown = percent.clamp(0, 100)
    level = if percent >= 90 then "nogo" elsif percent >= 70 then "hold" else "go" end
    circumference = 2 * Math::PI * 6
    tag.svg(class: "donut donut--#{level}", width: 16, height: 16, viewBox: "0 0 16 16", role: "img", "aria-label": "#{percent}%") do
      tag.circle(cx: 8, cy: 8, r: 6, class: "donut__track") +
        tag.circle(cx: 8, cy: 8, r: 6, class: "donut__fill", "stroke-dasharray": "#{(circumference * shown / 100).round(2)} #{circumference.round(2)}",
                   transform: "rotate(-90 8 8)")
    end
  end
end
