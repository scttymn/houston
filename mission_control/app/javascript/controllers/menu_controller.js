import { Controller } from "@hotwired/stimulus"

// The phone menu (the design's MobileMenu): a full-screen panel over the
// page, opened by the top bar's menu button and closed by its × or Escape.
export default class extends Controller {
  static targets = [ "button", "panel", "close" ]

  open() {
    this.panelTarget.hidden = false
    this.buttonTarget.setAttribute("aria-expanded", "true")
    document.documentElement.classList.add("menu-open")
    this.closeTarget.focus()
  }

  close() {
    if (this.panelTarget.hidden) return
    this.panelTarget.hidden = true
    this.buttonTarget.setAttribute("aria-expanded", "false")
    document.documentElement.classList.remove("menu-open")
    this.buttonTarget.focus()
  }

  disconnect() {
    document.documentElement.classList.remove("menu-open")
  }
}
