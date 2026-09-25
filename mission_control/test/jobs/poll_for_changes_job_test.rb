require "test_helper"
require_relative "../support/project_helpers"

# A missed or blocked webhook isn't a missed deploy (security fixes, M4):
# projects that deploy on push are also checked every few minutes. Projects
# whose webhook never arrived aren't: they don't deploy on push yet.
class PollForChangesJobTest < ActiveJob::TestCase
  include ProjectHelpers

  test "checks the projects that deploy on push" do
    pushed = make_linked_project("garage").tap { |p| p.update!(webhook_verified_at: 1.day.ago) }
    make_linked_project("quiet")
    make_project("unlinked").update!(webhook_verified_at: 1.day.ago)

    assert_enqueued_jobs 1, only: CheckForChangesJob do
      PollForChangesJob.perform_now
    end
    assert_enqueued_with job: CheckForChangesJob, args: [ pushed.id ]
  end

  test "it's scheduled" do
    schedule = YAML.load_file(Rails.root.join("config/recurring.yml"), aliases: true).dig("production", "poll_for_changes")
    assert_equal "PollForChangesJob", schedule["class"]
    assert_match(/every 10 minutes/, schedule["schedule"])
  end
end
