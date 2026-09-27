require "test_helper"
require_relative "../support/project_helpers"

# The copy's proposal (docs/plans/copy-project.md, Batch 2): a push whose
# compose.yml names a new project holds its deploy, and the project's page
# and the board say so, offering the copy only for a name that's free.
class ProjectCopyProposalTest < ActionDispatch::IntegrationTest
  include ProjectHelpers

  setup do
    @project = make_linked_project("equip-go")
    make_deploy(@project, 1, "go")
    @hold = make_deploy(@project, 2, "hold", error: 'compose.yml names "equip", not "equip-go"', started: 1.minute.ago)
    @hold.update!(proposed_name: "equip")
    sign_in_as users(:one)
  end

  test "a free name: the page offers the copy" do
    get project_path("equip-go")
    assert_select ".project-head .state--hold", "HOLD"
    assert_select ".notice--hold", /compose\.yml on main names equip.*equip-go keeps serving #{@project.running_deploy.short_sha}/m
    assert_select ".notice--hold a[href=?]", new_project_copy_path("equip-go"), text: "Copy equip-go to equip"

    get root_path
    assert_select "[data-project='equip-go'] .state--hold", "HOLD"
    assert_select "[data-project='equip-go']", /names equip/
  end

  test "a taken or reserved name: said, with nothing to click" do
    make_project("equip")
    get project_path("equip-go")
    assert_select ".notice--hold", /equip is another project/
    assert_select ".notice--hold a[href=?]", new_project_copy_path("equip-go"), 0

    @hold.update!(proposed_name: "admin")
    get project_path("equip-go")
    assert_select ".notice--hold", /admin can't be a project's name/
    assert_select ".notice--hold a[href=?]", new_project_copy_path("equip-go"), 0
  end

  test "a later deploy clears it" do
    make_deploy(@project, 3, "go")
    get project_path("equip-go")
    assert_select ".notice--hold", 0
  end
end
