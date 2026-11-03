<script lang="ts">
  // Canary: a second instance with a new version for me, a list of users
  // or a share of them. Both versions side by side, the share changes on the
  // fly; "Перевести всех" and "Отменить" are held. Autocancel follows the
  // autorollback rules; auto promote after N minutes is optional.
  import { api, errorText, type Release } from '../../lib/api/client'
  import { store } from '../../lib/api/stores.svelte'
  import { utc } from '../../lib/format'
  import Box from '../../lib/ui/Box.svelte'
  import HoldButton from '../../lib/ui/HoldButton.svelte'

  let { botId, releases, onchange }: { botId: number; releases: Release[]; onchange: () => void } = $props()
  type C = { version: string; share: number; users: number[]; auto_after: number; started_at: string; running: boolean }
  type Totals = Record<string, number>
  let cn = $state<C | null>(null)
  let totals = $state<Totals>({})
  let version = $state('')
  let me = $state(true)
  let usersText = $state('')
  let share = $state(10)
  let auto = $state(false)
  let autoMin = $state(30)
  let busy = $state(false)
  let error = $state('')

  async function load() {
    try {
      const r = await api.get<{ canary: C | null }>(`/api/bots/${botId}/canary`)
      cn = r.canary
      if (cn) {
        share = cn.share
        usersText = cn.users.join(', ')
        const m = await api.get<{ series: { totals: Totals } }>(`/api/bots/${botId}/metrics?period=1h`)
        totals = m.series.totals
      }
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void botId
    load()
    const t = setInterval(load, 10000)
    return () => clearInterval(t)
  })

  const ids = () =>
    usersText
      .split(/[\s,]+/)
      .map((s) => Number(s.replace('@', '')))
      .filter((n) => Number.isInteger(n) && n > 0)

  async function run(fn: () => Promise<unknown>) {
    busy = true
    error = ''
    try {
      await fn()
      store.refresh(botId)
      await load()
      onchange()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
  const start = () =>
    run(() => api.post(`/api/bots/${botId}/canary`, { version, share, users: ids(), me, auto_after: auto ? autoMin : 0 }))
  const update = () => run(() => api.patch(`/api/bots/${botId}/canary`, { share, users: ids(), auto_after: cn?.auto_after ?? 0 }))
  const promote = () => run(() => api.post(`/api/bots/${botId}/canary/promote`))
  const cancel = () => run(() => api.del(`/api/bots/${botId}/canary`))

  const candidates = $derived(releases.filter((r) => !r.current))
  $effect(() => {
    if (!version && candidates.length) version = candidates[0].version
  })
  // Main = all minus the canary.
  const side = (canary: boolean) => {
    const g = (k: string) => totals[(canary ? 'canary_' : '') + k] ?? 0
    const up = canary ? g('updates') : (totals.updates ?? 0) - (totals.canary_updates ?? 0)
    const err = canary ? g('errors') + g('timeouts') : (totals.errors ?? 0) + (totals.timeouts ?? 0) - (totals.canary_errors ?? 0) - (totals.canary_timeouts ?? 0)
    return { up, err, rate: up ? ((err / up) * 100).toFixed(1) + '%' : '—' }
  }
</script>

<Box title="Канарейка">
  {#if error}<p class="danger">{error}</p>{/if}
  {#if !cn}
    {#if candidates.length === 0}
      <p class="muted">Загрузите новую версию, чтобы запустить её рядом с текущей.</p>
    {:else}
      <div class="form">
        <label>Версия
          <select bind:value={version}>{#each candidates as r}<option value={r.version}>{r.version}</option>{/each}</select>
        </label>
        <label class="check"><input type="checkbox" bind:checked={me} /> я (Telegram ID из настроек)</label>
        <label>Пользователи (id через запятую)<input bind:value={usersText} placeholder="12345, 67890" /></label>
        <label>Доля остальных, %<input type="number" min="0" max="100" bind:value={share} /></label>
        <label class="check">
          <input type="checkbox" bind:checked={auto} /> перевести всех через
          <input class="n" type="number" min="5" bind:value={autoMin} disabled={!auto} /> мин, если падений нет и ошибок не больше
        </label>
        <p class="muted">Личные сообщения, кнопки и платежи этих пользователей идут в канарейку; группы и каналы — в основную версию. Канарейка отменяется сама по правилам автоотката.</p>
        <div><HoldButton danger={false} disabled={busy} onconfirm={start}>Запустить канарейку</HoldButton></div>
      </div>
    {/if}
  {:else}
    <p>
      <span class="num">{cn.version}</span> с <span class="num">{utc(cn.started_at)}</span>
      {#if !cn.running}<span class="danger">не работает</span>{/if}
      {#if cn.auto_after}<span class="muted">· автоперевод через {cn.auto_after} мин</span>{/if}
    </p>
    <table>
      <thead><tr><th></th><th>основная</th><th>канарейка</th></tr></thead>
      <tbody>
        <tr><td class="muted">апдейты за час</td><td class="num">{side(false).up}</td><td class="num">{side(true).up}</td></tr>
        <tr><td class="muted">ошибки</td><td class="num">{side(false).err} ({side(false).rate})</td><td class="num">{side(true).err} ({side(true).rate})</td></tr>
      </tbody>
    </table>
    <div class="line">
      <label>Доля, % <input class="n" type="number" min="0" max="100" bind:value={share} /></label>
      <input class="grow" bind:value={usersText} placeholder="пользователи" />
      <button disabled={busy} onclick={update}>Изменить</button>
    </div>
    <div class="line">
      <HoldButton disabled={busy} onconfirm={promote}>Перевести всех</HoldButton>
      <HoldButton disabled={busy} onconfirm={cancel}>Отменить</HoldButton>
    </div>
  {/if}
</Box>

<style>
  .form,
  label {
    display: flex;
    flex-direction: column;
    gap: var(--s1);
  }
  .form {
    gap: var(--s2);
  }
  .check {
    flex-direction: row;
    align-items: center;
    flex-wrap: wrap;
  }
  .line {
    display: flex;
    gap: var(--s2);
    align-items: center;
    margin-top: var(--s2);
  }
  .line label {
    flex-direction: row;
    align-items: center;
  }
  .grow {
    flex: 1;
  }
  .n {
    width: 5em;
  }
  table {
    width: 100%;
  }
  th,
  td {
    text-align: left;
    padding: var(--s1) var(--s2);
    border-bottom: var(--line);
  }
  th {
    color: var(--muted);
    font-weight: 400;
  }

  /* Phones: one column, rows of controls wrap. */
  @media (max-width: 700px) {
    .line {
      flex-wrap: wrap;
    }
  }
</style>
