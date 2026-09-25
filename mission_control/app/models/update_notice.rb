# Whether the flight board says an update is out: only when this server runs a
# release (vX.Y.Z) and the latest is newer by version number. A server built
# from a checkout, or dev, gets no note.
module UpdateNotice
  RELEASE = /\Av(\d+)\.(\d+)\.(\d+)\z/

  # latest, when it's newer than current; else nil.
  def self.newer(current, latest)
    c, l = numbers(current), numbers(latest)
    c && l && (l <=> c) == 1 ? latest : nil
  end

  def self.release?(version) = numbers(version).present?

  def self.numbers(version) = version.to_s.match(RELEASE)&.captures&.map(&:to_i)
  private_class_method :numbers
end
