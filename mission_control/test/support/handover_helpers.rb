# Handover.new answering a fake for a block, so a test decides how the hosts
# move without kamal-proxy or Cloudflare.
module HandoverHelpers
  def with_handover(fake)
    Handover.define_singleton_method(:new) { |*| fake }
    yield
  ensure
    Handover.singleton_class.send(:remove_method, :new)
  end

  # A fake whose forward! and back! run the blocks given.
  def handover_doing(forward: -> { [] }, back: -> { })
    Object.new.tap do |o|
      o.define_singleton_method(:forward!, &forward)
      o.define_singleton_method(:back!, &back)
    end
  end
end
