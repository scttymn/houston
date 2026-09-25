# Settings › Cloudflare in words (docs/plans/cloudflare-settings.md).
module CloudflareHelper
  RouteRow = Data.define(:hostname, :path, :goes_to, :service, :state)

  # The tunnel's routes as rows: which name and path, and where it goes, in
  # words, with the service address in small print. A hostname's rule with
  # no path after one with a path catches "anything else".
  def cloudflare_route_rows(view)
    seen = []
    rows = view.routes.map { |r| [ r, (:drift if r.drift) ] } + view.missing_routes.map { |r| [ r, :missing ] }
    rows.map do |route, state|
      path = route_path(route, after_a_path: seen.include?(route.hostname))
      seen << route.hostname
      RouteRow.new(hostname: route.hostname, path:, goes_to: route_destination(route), service: (route.service unless route.service.start_with?("http_status:")), state:)
    end
  end

  # Houston's records: its own names first, then each project's together.
  def cloudflare_record_rows(view)
    order = { "admin" => 0, "hooks" => 1, "wildcard" => 2 }
    view.records.sort_by { |r| [ order.fetch(r.project, 3), r.project, r.name ] }
  end

  def cloudflare_record_for(record)
    { "admin" => "Mission Control", "hooks" => "Webhooks", "wildcard" => "Your apps (every name)", "houston" => "Houston" }.fetch(record.project) { record.project }
  end

  private
    def route_path(route, after_a_path:)
      return "/<project>" if route.path == "^/[a-z0-9-]+$"
      return route.path if route.path.present?
      after_a_path ? "anything else" : "any"
    end

    def route_destination(route)
      if route.service.start_with?("http_status:") then "Nothing (#{route.service.delete_prefix("http_status:")})"
      elsif route.service == TunnelRoutes.mission_control
        if route.path.present? then "Webhooks"
        elsif route.hostname.to_s.start_with?("admin.") then "Mission Control"
        else "Maintenance page"
        end
      elsif route.hostname.nil? then "Your apps"
      else route.service
      end
    end
end
