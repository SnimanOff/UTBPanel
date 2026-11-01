<script lang="ts">
  // Server: host charts, processes, bots by load and revenue. Backups and
  // shared alert rules get their places here later.
  import { api, errorText, type Schemas } from '../lib/api/client'
  import { store } from '../lib/api/stores.svelte'
  import { bytes, compact } from '../lib/format'
  import { live } from '../lib/live.svelte'
  import { router } from '../lib/router.svelte'
  import Box from '../lib/ui/Box.svelte'
  import Chart from '../lib/ui/Chart.svelte'
  import Tile from '../lib/ui/Tile.svelte'

  type Server = Schemas['ServerMetrics']
  type Load = { bot_id: number; updates_24h: number; errors_24h: number; cpu_max: number; rss_max: number; revenue_24h: Record<string, number> }
  let period = $state('24h')
  let data = $state<Server>()
  let loads = $state<Load[]>([])
  let sortKey = $state<keyof Load>('updates_24h')
  let error = $state('')

  async function load() {
    error = ''
    try {
      ;[data, loads] = await Promise.all([
        api.get<Server>(`/api/server/metrics?period=${period}`),
        api.get<Load[]>('/api/server/bots'),
      ])
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void period
    load()
    store.load()
    const t = setInterval(load, 60_000)
    return () => clearInterval(t)
  })

  const s = $derived(live.server)
  const x = $derived((data?.points ?? []).map((p) => p.ts))
  const g = (k: string) => (data?.points ?? []).map((p) => p.g[k] ?? null)
  const counts = $derived.by(() => {
    const c = { running: 0, paused: 0, stopped: 0, error: 0 }
    for (const b of store.bots) {
      if (b.state === 'crashed' || b.state === 'error') c.error++
      else c[b.state as 'running' | 'paused' | 'stopped']++
    }
    return c
  })
  const rows = $derived(
    [...loads].sort((a, b) => {
      const v = (l: Load) => (sortKey === 'revenue_24h' ? Object.values(l.revenue_24h).reduce((x, y) => x + y, 0) : (l[sortKey] as number))
      return v(b) - v(a)
    }),
  )
  const name = (id: number) => store.byId(id)?.name ?? `#${id}`
</script>

<h1>Сервер</h1>
{#if error}<p class="danger">{error}</p>{/if}

<div class="tiles">
  <Tile label="CPU" value={s ? Math.round(s.cpu) + '%' : '—'} />
  <Tile label="RAM" value={s ? Math.round(s.ram) + '%' : '—'} />
  <Tile label="диск" value={s ? Math.round(s.disk) + '%' : '—'} bad={(s?.disk ?? 0) > 90} />
  <Tile label="сеть ↓/↑" value={s ? `${bytes(s.rx)}/с · ${bytes(s.tx)}/с` : '—'} />
  <Tile label="апд/с" value={s ? s.ups.toFixed(1) : '—'} />
  <Tile label="боты" value={`${counts.running} · ${counts.paused} · ${counts.stopped} · ${counts.error}`} hint="работают · пауза · стоп · ошибки" bad={counts.error > 0} />
  <Tile label="польз. за период" value={compact(data?.bots.uniques.period ?? 0)} hint="уникальные по всем ботам" />
</div>

<div class="periods">
  {#each ['1h', '24h', '7d', '30d'] as p}
    <button class:active={period === p} onclick={() => (period = p)}>{p.replace('h', 'ч').replace('d', 'д')}</button>
  {/each}
</div>

<div class="charts">
  <div class="chart"><h2>CPU и RAM, %</h2><Chart {x} unit="%" series={[{ label: 'CPU', values: g('cpu') }, { label: 'RAM', values: g('ram') }, { label: 'диск', values: g('disk') }]} /></div>
  <div class="chart"><h2>Сеть, байт/с</h2><Chart {x} series={[{ label: '↓', values: g('net_rx') }, { label: '↑', values: g('net_tx') }]} /></div>
  <div class="chart"><h2>Диск, байт/с</h2><Chart {x} series={[{ label: 'чтение', values: g('disk_read') }, { label: 'запись', values: g('disk_write') }]} /></div>
  <div class="chart">
    <h2>Процессы: RSS</h2>
    <Chart {x} series={['edge', 'core', 'panel'].map((p) => ({ label: p, values: g(`proc_${p}_rss`) }))} />
  </div>
</div>

<div class="grid">
  <Box title="Боты за 24 часа">
    <table>
      <thead>
        <tr>
          <th>Бот</th>
          {#each [['updates_24h', 'апдейты'], ['errors_24h', 'ошибки'], ['cpu_max', 'CPU макс'], ['rss_max', 'RAM макс'], ['revenue_24h', 'доход']] as [k, l]}
            <th class="sortable" class:active={sortKey === k} onclick={() => (sortKey = k as keyof Load)}>{l}</th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.bot_id)}
          <tr class="clickable" onclick={() => router.go(`/bots/${r.bot_id}`)}>
            <td>{name(r.bot_id)}</td>
            <td class="num">{r.updates_24h}</td>
            <td class="num" class:danger={r.errors_24h > 0}>{r.errors_24h}</td>
            <td class="num">{Math.round(r.cpu_max)}%</td>
            <td class="num">{bytes(r.rss_max)}</td>
            <td class="num">{Object.entries(r.revenue_24h).map(([c, v]) => `${v} ${c}`).join(' · ') || '—'}</td>
          </tr>
        {:else}
          <tr><td colspan="6" class="muted">Данных пока нет</td></tr>
        {/each}
      </tbody>
    </table>
  </Box>
  <Box title="Процессы">
    {#each data?.procs ?? [] as p}
      <div class="row"><span>{p.Name}</span><span class="num">{Math.round(p.CPUPercent ?? 0)}% · {bytes(p.RSS)}</span></div>
    {/each}
  </Box>
</div>

<style>
  h1 {
    margin-bottom: var(--s3);
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    gap: var(--s2);
    margin-bottom: var(--s3);
  }
  .periods {
    display: flex;
    margin-bottom: var(--s3);
  }
  .periods button + button {
    border-left: none;
  }
  .periods .active {
    border-color: var(--accent);
    background: var(--surface-2);
  }
  .charts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(420px, 1fr));
    gap: var(--s3);
    margin-bottom: var(--s3);
  }
  .chart {
    border: var(--line);
    background: var(--surface);
    padding: var(--s2) var(--s3);
  }
  h2 {
    font-size: var(--fs-xs);
    margin-bottom: var(--s1);
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 300px;
    gap: var(--s3);
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
  .sortable {
    cursor: pointer;
  }
  th.active {
    color: var(--text);
  }
  tr.clickable {
    cursor: pointer;
  }
  tr.clickable:hover td {
    background: var(--surface-2);
  }
  .row {
    display: flex;
    justify-content: space-between;
    padding: 2px 0;
  }
</style>
