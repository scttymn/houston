import { Controller } from "@hotwired/stimulus"

// Marks the menu link of the section in view. The links are plain anchors,
// so the menu works without this; this only moves the highlight.
//
// A clicked link stays marked until the reader scrolls by hand: the last
// sections can't reach the top of the window (the page ends first), so
// "the section at the top" would otherwise mark the one above them. At the
// very bottom, the last section is the one in view.
export default class extends Controller {
  static TOP = 120 // px below the window's top that counts as "in view"

  connect() {
    this.links = [ ...this.element.querySelectorAll("a[href*='#']") ]
    this.sections = this.links.map((link) => document.getElementById(link.hash.slice(1))).filter(Boolean)
    if (this.sections.length === 0) return

    this.chosen = this.sections.some((section) => `#${section.id}` === location.hash) ? location.hash.slice(1) : null
    this.onScroll = () => {
      if (this.pending) return
      this.pending = true
      requestAnimationFrame(() => { this.pending = false; this.update() })
    }
    this.release = () => { this.chosen = null }
    // A page refresh in place (a Turbo morph, as Check for updates does)
    // puts the menu back as the server sent it, unmarked: mark it again.
    this.onMorph = () => this.update()
    document.addEventListener("turbo:morph", this.onMorph)
    window.addEventListener("scroll", this.onScroll, { passive: true })
    for (const event of [ "wheel", "touchmove", "keydown" ]) window.addEventListener(event, this.release, { passive: true })
    this.update()
  }

  disconnect() {
    document.removeEventListener("turbo:morph", this.onMorph)
    window.removeEventListener("scroll", this.onScroll)
    for (const event of [ "wheel", "touchmove", "keydown" ]) window.removeEventListener(event, this.release)
  }

  choose(event) {
    this.chosen = event.currentTarget.hash.slice(1)
    this.mark(this.chosen)
  }

  update() {
    if (this.chosen) return this.mark(this.chosen)

    const root = document.documentElement
    const atBottom = window.innerHeight + window.scrollY >= root.scrollHeight - 2
    const current = atBottom
      ? this.sections[this.sections.length - 1]
      : [ ...this.sections ].reverse().find((section) => section.getBoundingClientRect().top <= this.constructor.TOP) || this.sections[0]
    this.mark(current.id)
  }

  mark(id) {
    this.links.forEach((link) => link.classList.toggle("is-current", link.hash === `#${id}`))
  }
}
