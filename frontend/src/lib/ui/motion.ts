import { fly } from 'svelte/transition'
import { cubicOut } from 'svelte/easing'

// Entrance for panels, cards and feed rows. Svelte's own transitions know
// nothing about the CSS tokens, so the one query lives here; a live
// MediaQueryList follows the OS setting without re-reading it per element.
const reduced = typeof matchMedia === 'function' ? matchMedia('(prefers-reduced-motion: reduce)') : undefined

export function fadeSlide(node: Element, { y = 6, duration = 180 } = {}) {
  // Nothing to compute when nothing moves: fly() would still resolve the
  // computed style of every element that enters.
  if (reduced?.matches) return { duration: 0 }
  return fly(node, { y, duration, easing: cubicOut })
}
