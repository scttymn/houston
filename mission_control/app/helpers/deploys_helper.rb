module DeploysHelper
  STATE_LABELS = { queued: "QUEUED", in_flight: "IN FLIGHT", no_go: "NO-GO", go: "GO", standby: "STANDBY" }.freeze

  def state_chip(status, tag: :span)
    status = status.to_sym
    content_tag(tag, STATE_LABELS.fetch(status), class: "mono state state--#{status.to_s.dasherize}")
  end

  # 108 → "1m 48s"
  def duration_words(seconds)
    seconds = seconds.to_i
    seconds < 60 ? "#{seconds}s" : "#{seconds / 60}m #{format("%02d", seconds % 60)}s"
  end

  # 52 → "T+00:52", as the deploy page counts
  def mission_elapsed(seconds)
    seconds = seconds.to_i
    format("T+%02d:%02d", seconds / 60, seconds % 60)
  end

  def app_url(host) = "https://#{host}"
end
