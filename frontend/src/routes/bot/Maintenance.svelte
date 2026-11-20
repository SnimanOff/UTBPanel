<script lang="ts">
  // Maintenance: the current state with "+15 / +30 / +1 ч" when it runs
  // late, and the form to plan new maintenance. Times are entered in UTC,
  // the bot's zone is shown next to them.
  import { api, errorText } from '../../lib/api/client'
  import { store } from '../../lib/api/stores.svelte'
  import { utc } from '../../lib/format'
  import { session } from '../../lib/session.svelte'
  import { defaultTZ, pair } from '../../lib/tz'
  import Box from '../../lib/ui/Box.svelte'
  import HoldButton from '../../lib/ui/HoldButton.svelte'

  let { botId }: { botId: number } = $props()
  type M = {
    kind: string
    state: string
    starts_at?: string
    ends_at?: string
    text: string
    version?: string
    auto_end?: boolean
    overdue?: boolean
  }
  let current = $state<M | null>(null)
  let loaded = $state(false)
  let busy = $state(false)
  let versions = $state<{ version: string; current: boolean }[]>([])
  let tz = $state(defaultTZ)
  let error = $state('')
  let saved = $state('')

  // Inputs of type datetime-local are read as UTC.
  const toInput = (d: Date) => d.toISOString().slice(0, 16)
  const fromInput = (s: string) => (s ? new Date(s + ':00Z') : null)
  const in1h = new Date(Math.ceil(Date.now() / 3600e3) * 3600e3 + 3600e3)
  let start = $state(toInput(in1h))
  let end = $state(toInput(new Date(in1h.getTime() + 3600e3)))
  let text = $state('')
  let version = $state('')
  let autoEnd = $state(true)
  let warnHours = $state(0)
  const canManage = $derived(session.scope?.role !== 'view')
  const stopped = $derived(store.byId(botId)?.state === 'stopped')

  async function loadCurrent(id: number) {
    try {
      const r = await api.get<{ maintenance: M | null }>(`/api/bots/${id}/maintenance`)
      if (id !== botId) return
      current = r.maintenance
      loaded = true
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void store.byId(botId)?.maintenance
    void loadCurrent(botId)
  })
  $effect(() => {
    const id = botId
    if (!canManage) return
    Promise.all([
      api.get<typeof versions>(`/api/bots/${id}/releases`),
      api.get<{ tz?: string }>(`/api/bots/${id}/settings`),
    ]).then(([releases, settings]) => {
      if (id !== botId) return
      versions = releases
      tz = settings.tz || defaultTZ
    }, (e) => { if (id === botId) error = errorText(e) })
  })

  async function call(fn: () => Promise<unknown>, msg: string) {
    if (busy) return
    const id = botId
    busy = true
    error = ''
    saved = ''
    try {
      await fn()
      await Promise.all([store.refresh(id), loadCurrent(id)])
      saved = msg
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }

  function schedule(e: SubmitEvent) {
    e.preventDefault()
    const s = fromInput(start), f = fromInput(end)
    if (!s || !f) return
    call(
      () =>
        api.post(`/api/bots/${botId}/maintenance`, {
          starts_at: s.toISOString(),
          ends_at: f.toISOString(),
          text,
          version: version || undefined,
          auto_end: !!version && autoEnd,
          warn_hours: warnHours || undefined,
        }),
      'Техработы запланированы',
    )
  }
  const extend = (minutes: number) =>
    call(() => api.post(`/api/bots/${botId}/maintenance/extend`, { minutes }), `Продлено на ${minutes} мин`)
  const startNow = () => call(() => api.post(`/api/bots/${botId}/pause`), 'Техработы начались')
  const endNow = () => call(() => api.post(`/api/bots/${botId}/resume`), current?.state === 'planned' ? 'План отменён' : 'Техработы завершены')

  const startD = $derived(fromInput(start))
  const endD = $derived(fromInput(end))
</script>

<div class="cols">
  <Box title="Сейчас">
    {#if !loaded}
      {#if !error}<p class="muted">Загрузка…</p>{/if}
    {:else if !current}
      <p class="muted">Техработ нет.</p>
      {#if canManage}
        <button class="primary" disabled={busy || stopped} title={stopped ? 'Сначала запустите бота' : undefined} onclick={startNow}>Начать техработы</button>
      {/if}
    {:else if current.kind === 'pause'}
      <p>Идут техработы без срока.</p>
      {#if canManage}<button disabled={busy} onclick={endNow}>Завершить техработы</button>{/if}
    {:else}
      <p>
        {current.state === 'planned' ? 'Запланированы' : 'Идут'}:
        <span class="num">{utc(current.starts_at)} – {utc(current.ends_at)}</span>
        {#if current.version}<span class="muted">→ {current.version}</span>{/if}
      </p>
      {#if current.starts_at}<p class="muted num">{pair(new Date(current.starts_at), tz)}</p>{/if}
      {#if current.overdue}<p class="warn">Не успели к сроку — пользователи видят старое время окончания.</p>{/if}
      {#if canManage}
        <div class="row">
          {#each [15, 30, 60] as m}<button disabled={busy} onclick={() => extend(m)}>+{m === 60 ? '1 ч' : m + ' мин'}</button>{/each}
          <HoldButton danger={false} disabled={busy} onconfirm={endNow}>{current.state === 'planned' ? 'Отменить план' : 'Завершить техработы'}</HoldButton>
        </div>
      {/if}
    {/if}
  </Box>

  {#if canManage}<Box title="Запланировать">
    <form onsubmit={schedule}>
      <label>
        Начало, UTC
        <input type="datetime-local" bind:value={start} />
        {#if startD}<span class="muted num">{pair(startD, tz)}</span>{/if}
      </label>
      <label>
        Окончание, UTC
        <input type="datetime-local" bind:value={end} />
        {#if endD}<span class="muted num">{pair(endD, tz)}</span>{/if}
      </label>
      <label>
        Текст для пользователей
        <textarea rows="2" bind:value={text} placeholder="Идут техработы до {'{end}'}. Скоро вернёмся."></textarea>
        <span class="muted">{'{start}'} и {'{end}'} — время в поясе бота ({tz})</span>
      </label>
      <label>
        Действие
        <select bind:value={version}>
          <option value="">только включить техработы</option>
          {#each versions.filter((v) => !v.current) as v}<option value={v.version}>и переключить на {v.version}</option>{/each}
        </select>
      </label>
      {#if version}
        <label class="check"><input type="checkbox" bind:checked={autoEnd} /> закончить, когда версия запустится</label>
      {/if}
      <label>
        Предупредить заранее рассылкой, часов
        <input type="number" min="0" max="72" bind:value={warnHours} />
      </label>
      <div class="row"><button class="primary" disabled={busy || !loaded || current?.state === 'active'}>Запланировать</button></div>
    </form>
  </Box>{/if}
</div>
{#if error}<p class="danger">{error}</p>{/if}
{#if saved}<p class="ok" role="status">{saved}</p>{/if}

<style>
  .cols {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--s3);
  }
  form,
  label {
    display: flex;
    flex-direction: column;
    gap: var(--s1);
  }
  form {
    gap: var(--s3);
  }
  .check {
    flex-direction: row;
    align-items: center;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s2);
    align-items: center;
  }
  .warn {
    color: var(--warn);
  }
  .ok {
    color: var(--accent-text);
  }

  /* Phones: one column, rows of controls wrap. */
  @media (max-width: 700px) {
    .cols {
      grid-template-columns: 1fr;
    }
  }
</style>
