// Who is looking at the panel: a gate key (the whole panel) or a person
// signed in with a passkey on the web listener, maybe with some bots only.
import { api, ApiError } from './api/client'

type Scope = { all: boolean; role: 'view' | 'manage' | 'full'; bots: number[] }

class Session {
  loaded = $state(false)
  /** The web listener answered "login_required". */
  login = $state(false)
  name = $state('')
  kind = $state('')
  source = $state('')
  scope = $state<Scope | null>(null)
  /** Version of the panel build (the release tag, "dev" for local builds). */
  version = $state('')
  commit = $state('')

  /** Sees only some bots or only some operations. */
  get limited() {
    return !!this.scope && !(this.scope.all && this.scope.role === 'full')
  }
  get owner() {
    return this.kind === 'owner'
  }
  get web() {
    return this.source === 'web'
  }

  async load() {
    try {
      const p = await api.get<{ pong: string; kind: string; source: string; scope?: Scope; version?: string; commit?: string }>('/api/ping')
      this.name = p.pong
      this.kind = p.kind
      this.source = p.source
      this.scope = p.scope ?? null
      this.version = p.version ?? ''
      this.commit = p.commit ?? ''
      this.login = false
    } catch (e) {
      this.login = e instanceof ApiError && e.code === 'login_required'
    } finally {
      this.loaded = true
    }
  }

  async logout() {
    await api.post('/web/logout').catch(() => {})
    location.href = '/'
  }
}

export const session = new Session()
