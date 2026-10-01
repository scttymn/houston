import { Controller } from "@hotwired/stimulus"

// A toast: says what just happened, then goes after a few seconds (or
// when it's clicked).
export default class extends Controller {
  static values = { after: { type: Number, default: 6000 } }

  connect() {
    this.timer = setTimeout(() => this.dismiss(), this.afterValue)
  }

  disconnect() {
    clearTimeout(this.timer)
  }

  dismiss() {
    this.element.remove()
  }
}
