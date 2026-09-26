import { Controller } from "@hotwired/stimulus"
import { Turbo } from "@hotwired/turbo-rails"

// Refreshes the page in place (Turbo morph) every `every` ms while this
// element is on the page. The server drops the element once there's nothing
// left to wait for, which stops the refreshing. It refreshes only when the
// page answers: while Mission Control restarts (a server update), a
// Cloudflare error page mustn't replace this one.
export default class extends Controller {
  static values = { every: Number }

  connect() {
    this.timer = setInterval(() => this.refresh(), this.everyValue)
  }

  disconnect() {
    clearInterval(this.timer)
  }

  async refresh() {
    try {
      const response = await fetch(window.location.href, { headers: { Accept: "text/html" }, cache: "no-store" })
      if (response.ok && !response.redirected) Turbo.visit(window.location.href, { action: "replace" })
    } catch {
      // Not answering yet: try again next time.
    }
  }
}
