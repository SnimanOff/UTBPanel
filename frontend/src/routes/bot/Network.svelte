<script lang="ts">
  // Network: external services of the bot seen by the proxy — connections,
  // error share, connect p95, traffic, when first seen.
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import { bytes } from '../../lib/format'

  let { botId }: { botId: number } = $props()
  type Service = Schemas['Service']
  let period = $state('24h')
  let list = $state<Service[]>([])
  let error = $state('')

  async function load() {
    error = ''
    try {
      list = await api.get<Service[]>(`/api/bots/${botId}/services?period=${period}`)
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void [botId, period]
    load()
  })

  async function markKnown(host: string) {
    try {
      await api.post(`/api/bots/${botId}/services/known`, { host })
      load()
    } catch (e) {
      error = errorText(e)
    }
  }
</script>

<div class="bar">
  {#each ['1h', '24h', '7d', '30d'] as p}
    <button class:active={period === p} onclick={() => (period = p)}>{p.replace('h', 'ч').replace('d', 'д')}</button>
  {/each}
  <span class="muted small">Содержимое запросов не видно: чужой трафик не расшифровывается.</span>
</div>
{#if error}<p class="danger">{error}</p>{/if}
<table>
  <thead>
    <tr><th>Адрес</th><th>Соединений</th><th>Ошибки</th><th>p95 соединения</th><th>Трафик ↓/↑</th><th>Впервые</th><th></th></tr>
  </thead>
  <tbody>
    {#each list as s (s.host)}
      <tr>
        <td class="mono">{s.host}</td>
        <td class="num">{s.connections}</td>
        <td class="num" class:danger={s.error_rate > 0.2}>{s.errors} · {Math.round(s.error_rate * 100)}%</td>
        <td class="num">{s.connect_p95_ms ? Math.round(s.connect_p95_ms) + ' мс' : '—'}</td>
        <td class="num">{bytes(s.bytes_in)} / {bytes(s.bytes_out)}</td>
        <td class="num muted">{s.first_seen?.slice(0, 16).replace('T', ' ') ?? '—'}</td>
        <td>
          {#if !s.known && s.first_seen}<button onclick={() => markKnown(s.host)}>известный</button>{/if}
        </td>
      </tr>
    {:else}
      <tr><td colspan="7" class="muted">Бот не подключался к внешним сервисам через прокси</td></tr>
    {/each}
  </tbody>
</table>

<style>
  .bar {
    display: flex;
    align-items: center;
    margin-bottom: var(--s3);
  }
  .bar button + button {
    border-left: none;
  }
  .bar .active {
    border-color: var(--accent);
    background: var(--surface-2);
  }
  .small {
    font-size: var(--fs-s);
    margin-left: var(--s3);
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
    font-size: var(--fs-s);
  }

  /* Phones: one column, rows of controls wrap. */
  @media (max-width: 700px) {
    .bar {
      flex-wrap: wrap;
    }
  }
</style>
