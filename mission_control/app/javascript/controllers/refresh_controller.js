import { Controller } from "@hotwired/stimulus"
import { Turbo } from "@hotwired/turbo-rails"

// Refreshes the page in place (Turbo morph) every `every` ms while this
// element is on the page. The server drops the element once there's nothing
// left to wait for, which stops the refreshing.
export default class extends Controller {
  static values = { every: Number }

  connect() {
    this.timer = setInterval(() => Turbo.visit(window.location.href, { action: "replace" }), this.everyValue)
  }

  disconnect() {
    clearInterval(this.timer)
  }
}
