import { api, errorText, type Schemas } from './api/client'

export type Action = Schemas['MCPAction']

// Actions an AI asked to confirm. Only the owner sees them (others get 403
// and an empty list). New requests also arrive as notifications, which
// reload this list at once; the timer catches expiry.
class Actions {
  items = $state<Action[]>([])
  error = $state('')
  busy = $state('')
  pending = $derived(this.items.filter((a) => a.state === 'pending'))
  private timer?: ReturnType<typeof setInterval>

  async load() {
    try {
      this.items = await api.get<Action[]>('/api/mcp/actions')
    } catch {
      this.items = []
    }
  }

  start() {
    if (this.timer) return
    this.load()
    this.timer = setInterval(() => this.load(), 15000)
  }

  async decide(id: string, confirm: boolean) {
    this.busy = id
    this.error = ''
    try {
      await api.post(`/api/mcp/actions/${id}/${confirm ? 'confirm' : 'reject'}`)
    } catch (e) {
      this.error = errorText(e)
    } finally {
      this.busy = ''
      await this.load()
    }
  }
}

export const actions = new Actions()
