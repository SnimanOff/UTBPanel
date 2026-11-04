import { api, errorText, type BotView, type Group } from './client'

/** Shared state: bots and groups, loaded once and refreshed on demand. */
class BotsStore {
  bots = $state<BotView[]>([])
  groups = $state<Group[]>([])
  /** Panics in the last 24 h by bot id (badges). */
  panics = $state<Record<string, number>>({})
  loaded = $state(false)
  error = $state('')
  private loading?: Promise<void>

  byId(id: number): BotView | undefined {
    return this.bots.find((b) => b.id === id)
  }

  load(): Promise<void> {
    this.loading ??= (async () => {
      try {
        const [bots, groups] = await Promise.all([
          api.get<BotView[]>('/api/bots'),
          // A person with some bots does not see groups.
          api.get<Group[]>('/api/groups').catch(() => [] as Group[]),
        ])
        this.bots = bots
        this.groups = groups
        this.loadPanics()
        this.error = ''
      } catch (e) {
        this.error = errorText(e)
      } finally {
        this.loaded = true
        this.loading = undefined
      }
    })()
    return this.loading
  }

  loadPanics() {
    api.get<Record<string, number>>('/api/panics/summary').then((p) => (this.panics = p), () => {})
  }

  /** Replace one bot after an action. */
  put(b: BotView) {
    const i = this.bots.findIndex((x) => x.id === b.id)
    if (i >= 0) this.bots[i] = b
    else this.bots.push(b)
  }

  async refresh(id: number) {
    try {
      this.put(await api.get<BotView>(`/api/bots/${id}`))
    } catch {
      /* keep the old state */
    }
  }
}

export const store = new BotsStore()

/** Poll the bot list while a page is open (live metrics come later). */
export function pollBots(ms = 5000): () => void {
  store.load()
  const t = setInterval(() => store.load(), ms)
  return () => clearInterval(t)
}
