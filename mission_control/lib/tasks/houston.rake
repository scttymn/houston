namespace :houston do
  desc "Print a new first-run setup code (only until the admin exists)"
  task setup_code: :environment do
    if User.exists?
      warn "setup is complete"
      exit 1
    end
    puts SetupCode.issue!
  end
end
