ENV["RAILS_ENV"] ||= "test"
require_relative "../config/environment"
require "rails/test_help"
require_relative "test_helpers/session_test_helper"
require "webmock/minitest"

module ActiveSupport
  class TestCase
    # Run tests in parallel with specified workers
    parallelize(workers: :number_of_processors)

    # Setup all fixtures in test/fixtures/*.yml for all tests in alphabetical order.
    fixtures :all

    # Rate limits and status checks live in the cache; start every test fresh.
    # The local registry answers by default; tests about it stub their own.
    setup do
      Rails.cache.clear
      stub_request(:get, "http://registry:5000/v2/").to_return(status: 200, body: "{}")
    end
  end
end
