require_relative "boot"
require "rails/all"

module Demo
  class Application < Rails::Application
    config.load_defaults 8.1
  end
end
