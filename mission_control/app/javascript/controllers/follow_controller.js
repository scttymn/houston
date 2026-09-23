import { Controller } from "@hotwired/stimulus"

// Keeps the deploy log scrolled to its end as chunks arrive, while Follow is
// on. Scrolling up turns it off; ticking it (or scrolling back to the end)
// turns it on again.
export default class extends Controller {
  static targets = [ "log", "toggle" ]

  connect() {
    this.observer = new MutationObserver(() => this.stick())
    this.observer.observe(this.logTarget, { childList: true, characterData: true, subtree: true })
    this.stick()
  }

  disconnect() {
    this.observer.disconnect()
  }

  toggle() {
    this.stick()
  }

  scrolled() {
    const log = this.logTarget
    this.toggleTarget.checked = log.scrollHeight - log.scrollTop - log.clientHeight < 24
  }

  stick() {
    if (this.toggleTarget.checked) this.logTarget.scrollTop = this.logTarget.scrollHeight
  }
}
