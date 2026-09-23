require "net/http"

module Cloudflare
  class Error < StandardError
    attr_reader :status

    def initialize(message, status:)
      super(message)
      @status = status
    end
  end

  # A small client for Cloudflare's v4 API: JSON in, the `result` out, and an
  # Error with Cloudflare's own message when it says no.
  class Client
    API = "https://api.cloudflare.com/client/v4"

    def initialize(token)
      @token = token
    end

    def get(path, **query) = request(Net::HTTP::Get, path, query:)
    def post(path, body) = request(Net::HTTP::Post, path, body:)
    def put(path, body) = request(Net::HTTP::Put, path, body:)
    def patch(path, body) = request(Net::HTTP::Patch, path, body:)

    private
      def request(verb, path, query: {}, body: nil)
        uri = URI("#{API}#{path}")
        uri.query = URI.encode_www_form(query) if query.any?
        request = verb.new(uri)
        request["Authorization"] = "Bearer #{@token}"
        request["Content-Type"] = "application/json"
        request.body = body.to_json if body

        response = Net::HTTP.start(uri.host, uri.port, use_ssl: true, open_timeout: 10, read_timeout: 10) { |http| http.request(request) }
        json = JSON.parse(response.body.presence || "{}") rescue {}
        unless response.is_a?(Net::HTTPSuccess) && json["success"]
          messages = Array(json["errors"]).filter_map { |e| e["message"] }
          raise Error.new(messages.presence&.join("; ") || "HTTP #{response.code}", status: response.code.to_i)
        end
        json["result"]
      rescue Net::OpenTimeout, Net::ReadTimeout, SocketError, SystemCallError, OpenSSL::SSL::SSLError => e
        raise Error.new("couldn't reach Cloudflare (#{e.class.name.demodulize})", status: 0)
      end
  end
end
