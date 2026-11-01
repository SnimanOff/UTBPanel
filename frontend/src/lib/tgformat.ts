// Preview of Telegram's HTML formatting: only the tags Telegram accepts are
// kept, everything else becomes text. Built from DOM nodes, never innerHTML
// of the source, so a broadcast text cannot run in the panel.

const allowed: Record<string, string> = {
  b: 'b', strong: 'b', i: 'i', em: 'i', u: 'u', ins: 'u', s: 's', strike: 's', del: 's',
  code: 'code', pre: 'pre', a: 'a', blockquote: 'blockquote', 'tg-spoiler': 'span',
}

function copy(from: Node, to: Node) {
  for (const n of Array.from(from.childNodes)) {
    if (n.nodeType === Node.TEXT_NODE) {
      const parts = (n.textContent ?? '').split('\n')
      parts.forEach((p, i) => {
        if (i > 0) to.appendChild(document.createElement('br'))
        to.appendChild(document.createTextNode(p))
      })
    } else if (n.nodeType === Node.ELEMENT_NODE) {
      const el = n as Element
      const tag = allowed[el.tagName.toLowerCase()]
      if (!tag) {
        copy(el, to)
        continue
      }
      const out = document.createElement(tag)
      if (tag === 'a') {
        const href = el.getAttribute('href') ?? ''
        if (/^(https?|tg):\/\//i.test(href)) {
          ;(out as HTMLAnchorElement).href = href
          ;(out as HTMLAnchorElement).target = '_blank'
          ;(out as HTMLAnchorElement).rel = 'noopener noreferrer'
        }
      }
      if (el.tagName.toLowerCase() === 'tg-spoiler') out.className = 'spoiler'
      copy(el, out)
      to.appendChild(out)
    }
  }
}

// render puts the preview of text into target.
export function render(target: HTMLElement, text: string, mode: string) {
  target.replaceChildren()
  if (mode !== 'HTML') {
    // MarkdownV2 is shown as is.
    target.style.whiteSpace = 'pre-wrap'
    target.appendChild(document.createTextNode(text))
    return
  }
  target.style.whiteSpace = ''
  const doc = new DOMParser().parseFromString(`<body>${text}</body>`, 'text/html')
  copy(doc.body, target)
}

// limit is Telegram's length limit: 4096 for a message, 1024 for a caption.
export function limit(withPhoto: boolean): number {
  return withPhoto ? 1024 : 4096
}
