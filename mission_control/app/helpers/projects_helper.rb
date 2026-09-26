module ProjectsHelper
  # A project's other services with their images: "db · postgres:17, cache".
  def services_words(project)
    project.accessories.map { |s| [ s, project.service_image(s) ].compact.join(" · ") }.join(", ").presence || "no services"
  end
end
