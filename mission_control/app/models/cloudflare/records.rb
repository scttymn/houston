module Cloudflare
  # DNS records in one zone. Houston's own records carry a comment starting
  # "managed-by:houston"; it never changes a record without one.
  class Records
    MANAGED = "managed-by:houston"

    def self.managed?(record)
      record["comment"].to_s.start_with?(MANAGED)
    end

    def initialize(client, zone)
      @client = client
      @zone = zone
    end

    def find(name)
      @client.get("/zones/#{@zone}/dns_records", name:).first
    end

    # A proxied CNAME from name to the tunnel: updates existing (one of
    # Houston's, already checked by the caller) or creates it.
    def point(name, tunnel_id, existing:, comment: MANAGED)
      record = { type: "CNAME", name:, content: "#{tunnel_id}.cfargotunnel.com", proxied: true, comment: }
      if existing
        @client.patch("/zones/#{@zone}/dns_records/#{existing["id"]}", record)
      else
        @client.post("/zones/#{@zone}/dns_records", record)
      end
    end

    def delete(record)
      @client.delete("/zones/#{@zone}/dns_records/#{record["id"]}")
    end
  end
end
