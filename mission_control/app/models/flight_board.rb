# The flight board's live updates (docs/plans/live-flight-board.md): a
# refresh, sent once the change is committed, so an open board re-fetches
# itself and morphs in place. Best effort: a failure is logged, never raised.
module FlightBoard
  STREAM = "flight_board"

  def self.refresh!
    ActiveRecord.after_all_transactions_commit { broadcast }
  end

  def self.broadcast
    Turbo::StreamsChannel.broadcast_refresh_to(STREAM)
  rescue StandardError => e
    Rails.logger.warn("the flight board's refresh wasn't sent: #{e.message}")
  end
  private_class_method :broadcast
end
