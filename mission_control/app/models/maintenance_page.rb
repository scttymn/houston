# What a project's hostnames show while it's in maintenance: its own page
# (x-houston.maintenance) with {{project}} and {{message}} filled in,
# HTML-escaped, or Houston's default. Nothing else in a page is interpreted.
class MaintenancePage
  def self.html(project, message: project.maintenance_message)
    return nil if project.maintenance_page.blank?

    project.maintenance_page
      .gsub("{{project}}", ERB::Util.html_escape(project.name))
      .gsub("{{message}}", ERB::Util.html_escape(message.to_s))
  end
end
