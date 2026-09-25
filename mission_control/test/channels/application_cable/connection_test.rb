require "test_helper"

# The live updates (flight board, deploy logs) take the same sessions pages
# do: not one made the other way (tunnel or not), and not an ended one
# (security fixes, review).
class ApplicationCable::ConnectionTest < ActionCable::Connection::TestCase
  test "a session made on the network connects on the network only" do
    session = users(:one).sessions.create!(tunnel: false, last_active_at: Time.current)
    cookies.signed[:session_id] = session.id
    connect
    assert_equal users(:one), connection.current_user

    assert_reject_connection { connect headers: { "Cf-Ray" => "8f-MCI" } }
  end

  test "a session made through the tunnel connects through it only" do
    session = users(:one).sessions.create!(tunnel: true, last_active_at: Time.current)
    cookies.signed[:session_id] = session.id
    connect headers: { "Cf-Ray" => "8f-MCI" }
    assert_equal users(:one), connection.current_user

    assert_reject_connection { connect }
  end

  test "an ended session doesn't connect, and is gone" do
    session = users(:one).sessions.create!(tunnel: false, last_active_at: (Session::IDLE + 1.hour).ago)
    cookies.signed[:session_id] = session.id
    assert_reject_connection { connect }
    assert_not Session.exists?(session.id)
  end
end
