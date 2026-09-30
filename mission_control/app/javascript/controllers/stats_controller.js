import { Controller } from "@hotwired/stimulus"
import { Turbo } from "@hotwired/turbo-rails"

// The flight board's resources, read while it's open (ResourcesController):
// at once, then every `every` ms while the tab is in view, and at once when
// it comes back into view. A board in a hidden tab, or closed, reads nothing,
// so with no one looking Mission Control doesn't read Docker.
export default class extends Controller {
  static values = { url: String, every: Number }

  connect() {
    this.whenVisible = () => { if (document.visibilityState === "visible") this.read() }
    document.addEventListener("visibilitychange", this.whenVisible)
    this.timer = setInterval(this.whenVisible, this.everyValue)
    this.whenVisible()
  }

  disconnect() {
    clearInterval(this.timer)
    document.removeEventListener("visibilitychange", this.whenVisible)
  }

  async read() {
    if (this.reading) return
    this.reading = true
    try {
      const response = await fetch(this.urlValue, { headers: { Accept: "text/vnd.turbo-stream.html" }, cache: "no-store" })
      if (response.ok && !response.redirected) Turbo.renderStreamMessage(await response.text())
    } catch {
      // Not answering (a server update): the next one tries again.
    } finally {
      this.reading = false
    }
  }
}
