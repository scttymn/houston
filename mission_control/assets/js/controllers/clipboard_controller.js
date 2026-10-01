import { Controller } from "@hotwired/stimulus"

// Copies the source target's text. If the browser refuses the clipboard, it
// selects the text so it can be copied by hand.
export default class extends Controller {
  static targets = [ "source", "button" ]

  async copy() {
    try {
      await navigator.clipboard.writeText(this.sourceTarget.textContent.trim())
      this.buttonTarget.textContent = "Copied"
    } catch {
      const range = document.createRange()
      range.selectNodeContents(this.sourceTarget)
      const selection = window.getSelection()
      selection.removeAllRanges()
      selection.addRange(range)
      this.buttonTarget.textContent = "Selected; press ⌘C"
    }
  }
}
