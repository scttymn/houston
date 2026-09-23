module ApplicationHelper
  def status_word(ok)
    tag.span(ok ? "GO" : "NO-GO", class: ok ? "status__go" : "status__nogo")
  end
end
