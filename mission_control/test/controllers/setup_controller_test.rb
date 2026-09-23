require "test_helper"

class SetupControllerTest < ActionDispatch::IntegrationTest
  setup do
    Session.delete_all
    User.delete_all
    @code = SetupCode.issue!
  end

  def details(overrides = {})
    { setup: { code: @code, email_address: "admin@example.com", password: "correct horse battery",
               password_confirmation: "correct horse battery" }.merge(overrides) }
  end

  test "shows the admin form" do
    get setup_path

    assert_response :success
    assert_select "h1", /Create the admin login/i
    assert_select "input[name='setup[code]']"
    assert_select "input[name='setup[email_address]'][type=email]"
    assert_select "input[name='setup[password]'][type=password]"
    assert_select "input[name='setup[password_confirmation]'][type=password]"
  end

  test "creates the admin and signs in" do
    post setup_path, params: details

    assert_redirected_to root_path
    assert_equal [ "admin@example.com" ], User.pluck(:email_address)
    assert cookies[:session_id].present?
    assert_equal 0, SetupCode.count, "the code is used up"
  end

  test "accepts the code in lower case and without the dash" do
    post setup_path, params: details(code: @code.downcase.delete("-"))

    assert_redirected_to root_path
    assert_equal 1, User.count
  end

  test "a wrong code creates nothing and keeps the code valid" do
    post setup_path, params: details(code: "AAAA-AAAA")

    assert_response :unprocessable_entity
    assert_select "form", /setup code/i
    assert_equal 0, User.count

    post setup_path, params: details
    assert_redirected_to root_path
  end

  test "invalid details create nothing" do
    [
      { email_address: "not-an-email" },
      { email_address: "" },
      { password: "short", password_confirmation: "short" },
      { password_confirmation: "something else entirely" }
    ].each do |bad|
      post setup_path, params: details(bad)

      assert_response :unprocessable_entity, "#{bad} should be rejected"
      assert_equal 0, User.count
      assert_equal 1, SetupCode.count, "#{bad} must not use up the code"
    end
  end

  test "setup is closed once an admin exists" do
    post setup_path, params: details
    delete session_path
    code = SetupCode.issue!

    get setup_path
    assert_redirected_to new_session_path

    post setup_path, params: details(code: code, email_address: "second@example.com")
    assert_redirected_to new_session_path
    assert_equal 1, User.count
  end

  test "losing the race to another setup creates nothing" do
    # The code matched, but another request consumed it before this one could.
    stale = SetupCode.last
    SetupCode.where(id: stale.id).delete_all

    original = SetupCode.method(:matching)
    SetupCode.define_singleton_method(:matching) { |_code| stale }
    begin
      post setup_path, params: details
    ensure
      SetupCode.singleton_class.send(:remove_method, :matching)
      SetupCode.define_singleton_method(:matching, original) unless SetupCode.respond_to?(:matching)
    end

    assert_response :unprocessable_entity
    assert_select "form", /already completed/i
    assert_equal 0, User.count
  end

  test "setup attempts are rate limited" do
    10.times { post setup_path, params: details(code: "AAAA-AAAA") }
    post setup_path, params: details(code: "AAAA-AAAA")

    assert_response :too_many_requests
  ensure
    Rails.cache.clear
  end
end
