ENV["RAILS_ENV"] ||= "test"
require_relative "../config/environment"
require "rails/test_help"
require_relative "test_helpers/session_test_helper"
require "webmock/minitest"
require "turbo/broadcastable/test_helper"

module ActiveSupport
  class TestCase
    # Run tests in parallel with specified workers
    parallelize(workers: :number_of_processors)

    # Setup all fixtures in test/fixtures/*.yml for all tests in alphabetical order.
    fixtures :all

    # Rate limits and status checks live in the cache; start every test fresh.
    # The local registry and admin.<base> answer by default; tests about them
    # stub their own.
    setup do
      Rails.cache.clear
      stub_local_services
    end

    def stub_local_services
      stub_request(:get, "http://registry:5000/v2/").to_return(status: 200, body: "{}")
      stub_request(:get, "https://admin.svnmns.com/ping").to_return(body: Installation.identity)
      stub_request(:get, "https://hooks.svnmns.com/ping").to_return(body: Installation.identity)
    end
  end
end
