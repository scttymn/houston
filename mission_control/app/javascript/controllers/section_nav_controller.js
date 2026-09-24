import { Controller } from "@hotwired/stimulus"

// Marks the menu link of the section in view. The links are plain anchors,
// so the menu works without this; this only moves the highlight.
export default class extends Controller {
  connect() {
    this.links = [ ...this.element.querySelectorAll("a[href*='#']") ]
    const sections = this.links.map((link) => document.getElementById(link.hash.slice(1))).filter(Boolean)
    if (sections.length === 0) return

    this.visible = new Set()
    this.observer = new IntersectionObserver((entries) => {
      entries.forEach((entry) => entry.isIntersecting ? this.visible.add(entry.target.id) : this.visible.delete(entry.target.id))
      const current = sections.find((section) => this.visible.has(section.id))
      if (current) this.mark(current.id)
    }, { rootMargin: "0px 0px -60% 0px" })
    sections.forEach((section) => this.observer.observe(section))
    this.mark(location.hash.slice(1) || sections[0].id)
  }

  disconnect() {
    this.observer?.disconnect()
  }

  mark(id) {
    this.links.forEach((link) => link.classList.toggle("is-current", link.hash === `#${id}`))
  }
}
