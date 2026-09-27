# Moves the hosts a copy's old and new projects share between their
# kamal-proxy services with no failed request, then their DNS records' owner
# (docs/plans/copy-project.md, spike notes). kamal-proxy refuses one host for
# two services, so a host is taken from one before it's given to the other;
# a catch-all to the taker's container answers in between, and is removed
# after. Each service is redeployed as Kamal left it (its target and options,
# read from kamal-proxy's state), changing only its hosts, and never with
# --force (it skips the health check, and answers 503s).
#
# forward! gives the new project the shared hosts (a copy's handover); back!
# returns them (Undo copy). A failure puts kamal-proxy and the records back
# as they were, and raises.
class Handover
  class Failed < StandardError; end

  STATE = "/home/kamal-proxy/.config/kamal-proxy/kamal-proxy.state"
  CATCH_ALL = "houston-handover"
  DEFAULT_BUFFER = 1_048_576

  # A host no one can ask for, kept by a service that has none of its own,
  # so it never becomes kamal-proxy's catch-all.
  def self.placeholder(name) = "#{name}.houston-copy.invalid"

  def initialize(copy, installation = Installation.current)
    @copy = copy
    @installation = installation
  end

  # Returns the hosts that moved.
  def forward!
    services = read_state
    taker = services["#{@copy.to}-web"] or raise Failed, "#{@copy.to}-web isn't in kamal-proxy yet"
    giver = services["#{@copy.from}-web"]
    wanted = @copy.project.hostnames(@installation)
    shared = giver ? giver[:hosts] & wanted : []
    move(giver:, taker:, shared:, giver_hosts: giver && (giver[:hosts] - shared), taker_hosts: wanted, giver_name: @copy.from)
    records(shared, from: @copy.from, to: @copy.to) { move_back(services) }
    @copy.update!(handed_over: shared, handed_over_at: Time.current)
    shared
  end

  def back!
    services = read_state
    shared = @copy.handed_over
    giver = services["#{@copy.to}-web"] or raise Failed, "#{@copy.to}-web isn't in kamal-proxy"
    taker = services["#{@copy.from}-web"] or raise Failed, "#{@copy.from}-web isn't in kamal-proxy any more: its container is gone"
    move(giver:, taker:, shared:, giver_hosts: giver[:hosts] - shared, taker_hosts: (taker[:hosts] + shared).uniq, giver_name: @copy.to)
    records(shared, from: @copy.to, to: @copy.from) { move_back(services) }
    @copy.update!(handed_over: [], handed_over_at: nil)
  end

  private
    def move(giver:, taker:, shared:, giver_hosts:, taker_hosts:, giver_name:)
      steps = []
      steps << deploy(CATCH_ALL, taker, hosts: []) if shared.any?
      steps << deploy(giver[:name], giver, hosts: giver_hosts.presence || [ self.class.placeholder(giver_name) ]) if giver && shared.any?
      steps << deploy(taker[:name], taker, hosts: taker_hosts)
      steps << "kamal-proxy remove #{CATCH_ALL}" if shared.any?
      ran = DockerCommand.run("exec", "kamal-proxy", "sh", "-c", steps.join(" && "), timeout: 120)
      return if ran.success

      move_back({ giver[:name] => giver, taker[:name] => taker }.compact) if giver
      raise Failed, "kamal-proxy didn't move the hosts: #{ran.output.to_s.lines.last(3).join.strip.truncate(500)}"
    end

    # Every service as it was, whatever got through: the one that took hosts
    # lets go of them first, then the other takes its own back.
    def move_back(services)
      steps = services.values.sort_by { |s| s[:name] == "#{@copy.from}-web" ? 1 : 0 }.map { |s| deploy(s[:name], s, hosts: s[:hosts]) }
      DockerCommand.run("exec", "kamal-proxy", "sh", "-c", [ *steps, "kamal-proxy remove #{CATCH_ALL} 2>/dev/null", "true" ].join("; "), timeout: 120)
    end

    def deploy(name, service, hosts:)
      o = service[:target_options]
      health = o[:health_check_config] || {}
      args = [ "kamal-proxy", "deploy", name, "--target", service[:target], *hosts.flat_map { |h| [ "--host", h ] },
               "--health-check-path", health[:path].presence || "/up" ]
      args += [ "--health-check-port", health[:port].to_s ] if health[:port].to_i.positive?
      args += [ "--health-check-host", health[:host] ] if health[:host].present?
      args += [ "--health-check-interval", ms(health[:interval]), "--health-check-timeout", ms(health[:timeout]), "--target-timeout", ms(o[:response_timeout]) ]
      args << "--buffer-requests" if o[:buffer_requests]
      args << "--buffer-responses" if o[:buffer_responses]
      args += [ "--buffer-memory", o[:max_memory_buffer_size].to_s ] if o[:max_memory_buffer_size].to_i.positive? && o[:max_memory_buffer_size] != DEFAULT_BUFFER
      args += [ "--max-request-body", o[:max_request_body_size].to_s ] if o[:max_request_body_size].to_i.positive?
      args += [ "--max-response-body", o[:max_response_body_size].to_s ] if o[:max_response_body_size].to_i.positive?
      args += o[:log_request_headers].to_a.flat_map { |h| [ "--log-request-header", h ] }
      args += o[:log_response_headers].to_a.flat_map { |h| [ "--log-response-header", h ] }
      args << "--forward-headers=#{o[:forward_headers] ? "true" : "false"}"
      args.map { |a| a.match?(/\A[\w.:\/=@-]+\z/) ? a : "'#{a.gsub("'", "'\\\\''")}'" }.join(" ")
    end

    def ms(ns) = "#{ns.to_i / 1_000_000}ms"

    # name → { name, hosts, target, target_options }, from kamal-proxy's state.
    def read_state
      ran = DockerCommand.run("exec", "kamal-proxy", "cat", STATE, timeout: 10)
      raise Failed, "couldn't read kamal-proxy's state: #{ran.output.to_s.strip.truncate(300)}" unless ran.success
      JSON.parse(ran.output).to_h do |s|
        s = s.deep_symbolize_keys
        [ s[:name], { name: s[:name], hosts: s.dig(:options, :hosts).to_a, target: s[:active_targets].to_a.first, target_options: s[:target_options].to_h } ]
      end
    rescue JSON::ParserError
      raise Failed, "kamal-proxy's state isn't JSON"
    end

    # The shared hosts' records, re-commented from one project to the other;
    # only records carrying exactly the first project's comment. A failure
    # re-comments what moved, runs undo (kamal-proxy back), and raises.
    def records(hosts, from:, to:)
      return if hosts.empty? || !@installation.connected? || @installation.cloudflare_api_token.blank?

      client = Cloudflare::Client.new(@installation.cloudflare_api_token)
      mine, theirs = "#{Cloudflare::Records::MANAGED} project:#{from}", "#{Cloudflare::Records::MANAGED} project:#{to}"
      moved = []
      begin
        hosts.each do |host|
          zone = zone_of(client, host) or next
          record = client.get("/zones/#{zone}/dns_records", name: host).first
          next unless record && record["comment"] == mine
          client.patch("/zones/#{zone}/dns_records/#{record["id"]}", { comment: theirs })
          moved << [ zone, record["id"] ]
        end
      rescue Cloudflare::Error => e
        moved.each do |zone, id|
          client.patch("/zones/#{zone}/dns_records/#{id}", { comment: mine })
        rescue Cloudflare::Error => again
          Rails.logger.error("houston: the record #{id} is left commented #{theirs}: #{again.message}")
        end
        yield
        raise Failed, "Cloudflare said no while moving the hosts' records: #{e.message}"
      end
    end

    def zone_of(client, host)
      return @installation.cloudflare_zone_id if host.end_with?(".#{@installation.base_domain}")
      labels = host.split(".")
      (0...(labels.size - 1)).each do |i|
        zone = client.get("/zones", name: labels[i..].join(".")).first
        return zone["id"] if zone
      end
      nil
    end
end
