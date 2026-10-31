import { api, errorText, type BotView, type Group } from './client'

/** Shared state: bots and groups, loaded once and refreshed on demand. */
class BotsStore {
  bots = $state<BotView[]>([])
  groups = $state<Group[]>([])
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
          api.get<Group[]>('/api/groups'),
        ])
        this.bots = bots
        this.groups = groups
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
