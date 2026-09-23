require "test_helper"
require_relative "../support/project_helpers"

class DeployPagesTest < ActionDispatch::IntegrationTest
  include ProjectHelpers

  setup do
    sign_in_as users(:one)
    @project = make_project("equip")
  end

  test "a deploy's page" do
    make_deploy(@project, 1, "go", sha: "a" * 40, log: "Deploy #1 of equip\n")
    make_deploy(@project, 2, "in_flight", sha: "b" * 40, step: "Build", log: "Deploy #2 of equip\nstep output\n")

    get project_deploy_path("equip", 2)
    assert_response :success
    assert_select "h1", /Deploy #2/i
    assert_select ".state", /IN FLIGHT/
    assert_select "body", /bbbbbbb/
    assert_select "body", /aaaaaaa is still serving/
    assert_select ".deploy-steps li:first-child[data-step=Test]", /00/
    assert_select ".deploy-steps [data-step=Secrets][data-state=done]"
    assert_select ".deploy-steps [data-step=Build][data-state=current]"
    # The pre-deploy snapshot comes after the build, before the accessories.
    assert_select ".deploy-steps [data-step=Build] + [data-step=Snapshot][data-state=pending] + [data-step=Accessories]"
    assert_select ".deploy-steps [data-step=Deploy][data-state=pending]"
    assert_select "pre.log", /step output/
    assert_select "turbo-cable-stream-source[signed-stream-name]"
    assert_select "#deploy_log"
    assert_select "#deploy_steps"
    assert_select "#deploy_status"
    assert_select "[data-controller~=refresh]", 0

    get project_deploy_path("equip", 1)
    assert_select ".state", /GO/
    assert_select ".deploy-steps [data-state=current]", 0
    assert_select "[data-controller~=refresh]", 0

    get project_deploy_path("equip", 99)
    assert_response :not_found
  end

  test "the log is text, not HTML" do
    make_deploy(@project, 1, "no_go", step: "Release", error: "release hook failed (exit 3)", log: "<script>alert('x')</script>\n")

    get project_deploy_path("equip", 1)

    assert_not_includes response.body, "<script>alert"
    assert_select "pre.log", /<script>alert/
    assert_select ".deploy-steps [data-step=Release][data-state=failed]"
    assert_select "body", /release hook failed \(exit 3\)/
  end

  test "a runner's deploy starts with its tests" do
    make_deploy(@project, 1, "in_flight", step: "Test").update!(runner: "houston-runner-1")
    get project_deploy_path("equip", 1)
    assert_select ".deploy-steps [data-step=Test][data-state=current]"
    assert_select ".deploy-steps [data-step=Secrets][data-state=pending]"

    make_deploy(@project, 2, "go")
    get project_deploy_path("equip", 2)
    assert_select ".deploy-steps [data-step=Test][data-state=skipped]", /SKIPPED/
  end

  # A restore's own steps: no tests, no build (an image pulled back), no hooks.
  test "a restore's page shows its steps" do
    make_deploy(@project, 1, "in_flight", step: "Restore data").update!(runner: "houston-runner-1", kind: "restore", generation: 2)
    get project_deploy_path("equip", 1)
    assert_select ".deploy-steps li", 7
    assert_select ".deploy-steps li:first-child[data-step=Prepare][data-state=done]", /00/
    assert_select ".deploy-steps [data-step=Image][data-state=done] + [data-step=Accessories][data-state=done] + " \
                  "[data-step='Restore data'][data-state=current] + [data-step='Safety snapshot'][data-state=pending] + " \
                  "[data-step=Switch] + [data-step='Clean up']"
    assert_select ".deploy-steps [data-step=Test]", 0
  end
end
