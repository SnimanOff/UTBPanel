// Live server and bot metrics: one SSE stream (/api/live) for the whole
// interface, once a second. The header and card mini charts read it.
import { subscribe } from './api/client'

export type ServerLive = { cpu: number; ram: number; disk: number; rx: number; tx: number; ups: number }
export type BotLive = {
  ups: number
  inflight: number
  resp_ms: number
  dropped: number
  cpu: number
  rss: number
  history: number[]
}
type Snapshot = { ts: number; server: ServerLive; bots: Record<string, Omit<BotLive, 'history'>> }

const KEEP = 60 // points in card mini charts

class Live {
  server = $state<ServerLive | undefined>(undefined)
  bots = $state<Record<number, BotLive>>({})
  connected = $state(false)
  /** Last 5 minutes of the server, for the "Live" chart. */
  serverHistory = $state<{ ts: number; s: ServerLive }[]>([])
  private stop?: () => void
  private users = 0

  private apply(s: Snapshot) {
    this.server = s.server
    this.serverHistory.push({ ts: s.ts, s: s.server })
    if (this.serverHistory.length > 300) this.serverHistory.splice(0, this.serverHistory.length - 300)
    const next: Record<number, BotLive> = {}
    for (const [id, b] of Object.entries(s.bots ?? {})) {
      const prev = this.bots[Number(id)]
      const history = [...(prev?.history ?? []), b.ups].slice(-KEEP)
      next[Number(id)] = { ...b, history }
    }
    this.bots = next
  }

  /** Start the stream; call the returned function to release it. */
  use(): () => void {
    if (this.users++ === 0) {
      this.stop = subscribe(
        '/api/live?history=1',
        {
          history: (h: Snapshot[]) => h.forEach((s) => this.apply(s)),
          live: (s: Snapshot) => this.apply(s),
        },
        (open) => (this.connected = open),
      )
    }
    return () => {
      if (--this.users === 0) this.stop?.()
    }
  }
}

export const live = new Live()
