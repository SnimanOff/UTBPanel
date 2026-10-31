<script lang="ts">
  // The action log with filters: bot, action, source, period; details
  // show the diff.
  import { api, errorText, type AuditEntry } from '../lib/api/client'
  import { store } from '../lib/api/stores.svelte'
  import { utc } from '../lib/format'

  let bot = $state(0)
  let action = $state('')
  let source = $state('')
  let from = $state('')
  let to = $state('')
  let entries = $state<AuditEntry[]>([])
  let more = $state(false)
  let open = $state<number | null>(null)
  let error = $state('')

  function query(before = 0) {
    const q = new URLSearchParams({ limit: '100' })
    if (bot) q.set('bot', String(bot))
    if (action) q.set('action', action)
    if (source) q.set('source', source)
    if (from) q.set('from', new Date(from + 'Z').toISOString())
    if (to) q.set('to', new Date(to + 'Z').toISOString())
    if (before) q.set('before', String(before))
    return q
  }

  async function load(append = false) {
    error = ''
    try {
      const before = append && entries.length ? entries[entries.length - 1].id : 0
      const page = await api.get<AuditEntry[]>(`/api/audit?${query(before)}`)
      entries = append ? [...entries, ...page] : page
      more = page.length === 100
    } catch (e) {
      error = errorText(e)
    }
  }

  $effect(() => {
    void [bot, action, source, from, to]
    load()
  })
  $effect(() => {
    store.load()
  })

  const botName = (id?: number) => (id ? (store.byId(id)?.name ?? `#${id}`) : '')
  const sources: Record<string, string> = { ui: 'интерфейс', mcp: 'MCP', cli: 'SSH', autorollback: 'автооткат', schedule: 'расписание', core: 'ядро' }
</script>

<h1>Аудит</h1>
<div class="filters">
  <select bind:value={bot}>
    <option value={0}>все боты</option>
    {#each store.bots as b}<option value={b.id}>{b.name}</option>{/each}
  </select>
  <input bind:value={action} placeholder="действие" />
  <select bind:value={source}>
    <option value="">все источники</option>
    {#each Object.entries(sources) as [k, v]}<option value={k}>{v}</option>{/each}
  </select>
  <label class="muted">с <input type="datetime-local" bind:value={from} /></label>
  <label class="muted">по <input type="datetime-local" bind:value={to} /></label>
  <span class="muted small">время в UTC</span>
</div>
{#if error}<p class="danger">{error}</p>{/if}

<table>
  <thead>
    <tr><th>Время (UTC)</th><th>Ключ</th><th>Источник</th><th>Действие</th><th>Бот</th><th>Объект</th><th>Итог</th></tr>
  </thead>
  <tbody>
    {#each entries as e (e.id)}
      <tr class:clickable={!!e.details} onclick={() => (open = open === e.id ? null : e.id)}>
        <td class="num">{utc(e.ts, true)}</td>
        <td>{e.key}</td>
        <td class="muted">{sources[e.source] ?? e.source}</td>
        <td class="mono">{e.action}</td>
        <td>{botName(e.bot_id)}</td>
        <td>{e.object ?? ''}</td>
        <td class:danger={e.result !== 'ok'}>{e.result}</td>
      </tr>
      {#if open === e.id && e.details}
        <tr><td colspan="7"><pre class="details">{JSON.stringify(e.details, null, 2)}</pre></td></tr>
      {/if}
    {:else}
      <tr><td colspan="7" class="muted">Записей нет</td></tr>
    {/each}
  </tbody>
</table>
{#if more}<button class="more" onclick={() => load(true)}>Ещё</button>{/if}

<style>
  h1 {
    margin-bottom: var(--s3);
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s2);
    align-items: center;
    margin-bottom: var(--s3);
  }
  .small {
    font-size: var(--fs-s);
  }
  th,
  td {
    text-align: left;
    padding: var(--s1) var(--s2);
    border-bottom: var(--line);
    vertical-align: top;
  }
  th {
    color: var(--muted);
    font-weight: 400;
    font-size: var(--fs-s);
  }
  tr.clickable {
    cursor: pointer;
  }
  tr.clickable:hover td {
    background: var(--surface);
  }
  .details {
    margin: 0;
    font-size: var(--fs-s);
    color: var(--muted);
  }
  .more {
    margin-top: var(--s3);
  }
</style>
