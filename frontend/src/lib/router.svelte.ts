// A tiny history router: the panel serves index.html for every unknown
// path, so plain paths work and reloads keep the page.

export type Route =
  | { name: 'home' }
  | { name: 'bot'; id: number; tab: string }
  | { name: 'server' }
  | { name: 'audit' }
  | { name: 'keys' }
  | { name: 'ai' }
  | { name: 'mini' }
  | { name: 'notfound' }

export function parse(path: string, search = ''): Route {
  const parts = path.split('/').filter(Boolean)
  const q = new URLSearchParams(search)
  switch (parts[0]) {
    case undefined:
      return { name: 'home' }
    case 'bots':
      if (parts[1] && /^\d+$/.test(parts[1])) {
        return { name: 'bot', id: Number(parts[1]), tab: parts[2] ?? q.get('tab') ?? 'logs' }
      }
      return { name: 'notfound' }
    case 'server':
    case 'audit':
    case 'keys':
    case 'ai':
    case 'mini':
      return { name: parts[0] }
  }
  return { name: 'notfound' }
}

class Router {
  route = $state<Route>(parse(location.pathname, location.search))

  constructor() {
    addEventListener('popstate', () => {
      this.route = parse(location.pathname, location.search)
    })
  }

  go(path: string, replace = false) {
    if (path === location.pathname + location.search) return
    if (replace) history.replaceState(null, '', path)
    else history.pushState(null, '', path)
    this.route = parse(location.pathname, location.search)
    if (!replace) scrollTo(0, 0)
  }
}

export const router = new Router()

/** Use on <a href> to navigate without reloading. */
export function link(node: HTMLAnchorElement) {
  const onclick = (e: MouseEvent) => {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    const href = node.getAttribute('href')
    if (!href || href.startsWith('http')) return
    e.preventDefault()
    router.go(href)
  }
  node.addEventListener('click', onclick)
  return { destroy: () => node.removeEventListener('click', onclick) }
}
