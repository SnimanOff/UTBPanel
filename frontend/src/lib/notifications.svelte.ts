import { api, type Schemas } from './api/client'
import { actions } from './actions.svelte'
import type { Item } from './ui/Feed.svelte'

type N = Schemas['Notification']

// The notification feed: loaded from the panel, new ones arrive by SSE.
// The bell counts unread ones; bots with an open error get a red frame.
class Notifications {
  items = $state<Item[]>([])
  problems = $state<number[]>([])
  unread = $derived(this.items.filter((i) => !i.read).length)
  private es?: EventSource

  private toItem(n: N): Item {
    return {
      id: n.id,
      ts: n.last_ts,
      text: n.count > 1 ? `${n.text} ×${n.count}` : n.text,
      severity: n.severity,
      href: n.href,
      read: n.read,
    }
  }

  async load() {
    try {
      const ns = await api.get<N[]>('/api/notifications?limit=200')
      this.items = ns.map((n) => this.toItem(n))
      this.problems = await api.get<number[]>('/api/notifications/problems')
    } catch {
      /* the bell stays as it was */
    }
  }

  /** Load and listen; call once at start. */
  start() {
    this.load()
    if (this.es || typeof EventSource === 'undefined') return
    this.es = new EventSource('/api/notifications/stream')
    this.es.addEventListener('notification', (e) => {
      const n = JSON.parse((e as MessageEvent).data) as N
      if (this.items.some((x) => x.id === n.id)) return
      this.items = [this.toItem(n), ...this.items].slice(0, 200)
      if (n.severity === 'error' && n.bot_id && !this.problems.includes(n.bot_id)) this.problems = [...this.problems, n.bot_id]
      if (n.severity === 'ok') this.load()
      if (n.kind === 'mcp_confirm') actions.load()
    })
    // Repeats are glued on the server: refresh counts now and then.
    setInterval(() => this.load(), 60000)
  }

  read(id: number) {
    const i = this.items.find((x) => x.id === id)
    if (i && !i.read) {
      i.read = true
      api.post('/api/notifications/read', { ids: [id] }).then(() => this.load(), () => {})
    }
  }

  readAll() {
    this.items.forEach((i) => (i.read = true))
    api.post('/api/notifications/read', { ids: [] }).then(() => this.load(), () => {})
  }

  hasProblem(botId: number) {
    return this.problems.includes(botId)
  }
}

export const notifications = new Notifications()
