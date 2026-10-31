// Live server and bot metrics. Filled from /api/live (SSE) once the panel
// serves it; until then the header shows dashes.

export type ServerLive = { cpu: number; ram: number; disk: number; rx: number; tx: number; ups: number }
export type BotLive = { ups: number; inflight: number; resp_ms: number; cpu: number; rss: number; history: number[] }

class Live {
  server = $state<ServerLive | undefined>(undefined)
  bots = $state<Record<number, BotLive>>({})
}

export const live = new Live()
