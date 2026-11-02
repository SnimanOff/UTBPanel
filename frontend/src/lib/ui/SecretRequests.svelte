<script lang="ts">
  // Secrets an AI asked for. The value is typed here by the owner and never
  // goes to the AI, which sees only the state. With botId: the requests of
  // one bot (the Settings tab; the notification highlights the field).
  // Without: all waiting requests, including tokens of new bots (the AI page).
  import { api, ApiError, errorText, type Schemas } from '../api/client'
  import { link } from '../router.svelte'
  import { store } from '../api/stores.svelte'
  import SecretField from './SecretField.svelte'

  type Req = Schemas['SecretRequest']
  let {
    botId = 0,
    highlight = '',
    onset,
  }: { botId?: number; highlight?: string; onset?: (name: string) => void } = $props()
  let items = $state<Req[]>([])
  let error = $state('')
  let fieldErrors = $state<Record<string, string>>({})

  async function load() {
    try {
      const all = await api.get<Req[]>(botId ? `/api/bots/${botId}/secret-requests` : '/api/secret-requests')
      items = all.filter((r) => r.state === 'waiting')
    } catch {
      items = []
    }
  }
  $effect(() => {
    void botId
    load()
    const t = setInterval(load, 30000)
    return () => clearInterval(t)
  })
  $effect(() => {
    if (!highlight || !items.length) return
    document.getElementById(`secret-request-${highlight}`)?.scrollIntoView({ block: 'center' })
  })

  async function save(r: Req, value: string) {
    error = ''
    fieldErrors = { ...fieldErrors, [r.id]: '' }
    try {
      if (r.bot_id) {
        await api.put(`/api/bots/${r.bot_id}/secrets/${encodeURIComponent(r.name)}`, { value })
      } else {
        await api.post(`/api/secret-requests/${r.id}/token`, { token: value })
        store.load()
      }
      onset?.(r.name)
      await load()
    } catch (e) {
      fieldErrors = { ...fieldErrors, [r.id]: e instanceof ApiError && e.code === 'telegram' ? 'Telegram: ' + e.message : errorText(e) }
      throw e
    }
  }
  async function reject(r: Req) {
    error = ''
    try {
      await api.post(`/api/secret-requests/${r.id}/reject`)
      await load()
    } catch (e) {
      error = errorText(e)
    }
  }
  const botName = (id?: number) => store.bots.find((b) => b.id === id)?.name ?? `бот ${id}`
  const hours = (ts: string) => Math.max(0, Math.round((new Date(ts).getTime() - Date.now()) / 3600000))
</script>

{#if items.length}
  <div class="requests">
    {#if error}<p class="danger">{error}</p>{/if}
    {#each items as r (r.id)}
      <div class="request" class:lit={highlight === r.name} id={`secret-request-${r.name}`}>
        <div class="head">
          <strong>
            {#if !r.bot_id}Токен нового бота «{r.bot_name}»
            {:else if botId}Нейросеть просит {r.name}
            {:else}<a href={`/bots/${r.bot_id}/settings?secret=${encodeURIComponent(r.name)}`} use:link>{botName(r.bot_id)}</a>: {r.name}{/if}
          </strong>
          <span class="muted">{r.requested_by} · ещё {hours(r.expires_at)} ч</span>
        </div>
        {#if r.hint}<p class="hint">{r.hint}</p>{/if}
        {#if !r.bot_id || botId}
          <SecretField name={r.name} onsave={(v) => save(r, v)} error={fieldErrors[r.id] ?? ''} />
          {#if !r.bot_id}<p class="muted small">Токен проверяется через getMe, бот создаётся сразу.</p>{/if}
        {/if}
        <button class="reject" onclick={() => reject(r)}>Отклонить</button>
      </div>
    {/each}
  </div>
{/if}

<style>
  .request {
    border: var(--line);
    border-color: var(--warn);
    padding: var(--s2);
    margin-bottom: var(--s2);
    display: flex;
    flex-direction: column;
    gap: var(--s1);
  }
  .request.lit {
    outline: 2px solid var(--accent-hi);
  }
  .head {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .head .muted,
  .small {
    font-size: var(--fs-s);
  }
  .hint {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .reject {
    align-self: flex-start;
  }
</style>
