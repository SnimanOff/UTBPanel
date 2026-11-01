<script lang="ts">
  // Tiles and charts on top of the bot page. Periods: Live (last 5 min,
  // per second) and history from the panel's metrics tables.
  import { api, errorText, type BotView, type Schemas } from '../../lib/api/client'
  import { bytes, compact, uptime } from '../../lib/format'
  import { live } from '../../lib/live.svelte'
  import Chart from '../../lib/ui/Chart.svelte'
  import Tile from '../../lib/ui/Tile.svelte'

  let { bot }: { bot: BotView } = $props()
  type BotMetrics = Schemas['BotMetrics']
  const periods = ['live', '1h', '24h', '7d', '30d', '90d', '1y', 'all']
  const labels: Record<string, string> = { live: 'Live', '1h': '1ч', '24h': '24ч', '7d': '7д', '30d': '30д', '90d': '90д', '1y': '1г', all: 'всё' }
  let period = $state(localStorage.getItem('period') ?? '24h')
  let data = $state<BotMetrics | undefined>()
  let day = $state<BotMetrics | undefined>() // for "revenue 24h" and DAU
  let error = $state('')

  async function load() {
    error = ''
    try {
      const p = period === 'live' ? '1h' : period
      data = await api.get<BotMetrics>(`/api/bots/${bot.id}/metrics?period=${p}`)
      day = p === '24h' ? data : await api.get<BotMetrics>(`/api/bots/${bot.id}/metrics?period=24h`)
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void bot.id
    void period
    try {
      localStorage.setItem('period', period)
    } catch {}
    load()
    const t = setInterval(load, 60_000)
    return () => clearInterval(t)
  })

  const l = $derived(live.bots[bot.id])
  const s = $derived(data?.series)
  const u = $derived(data?.users)
  const sum = (prefix: string, suffix: string, t?: Record<string, number>) =>
    Object.entries(t ?? {})
      .filter(([k]) => k.startsWith(prefix) && k.endsWith(suffix))
      .reduce((a, [, v]) => a + v, 0)
  const revenue = $derived.by(() => {
    const t = day?.series.totals ?? {}
    return Object.entries(t)
      .filter(([k]) => k.startsWith('pay_') && k.endsWith('_sum'))
      .map(([k, v]) => `${v} ${k.slice(4, -4)}`)
      .join(' · ') || '0'
  })
  const errors = $derived((s?.totals.errors ?? 0) + (s?.totals.timeouts ?? 0))
  const marks = $derived((s?.deploys ?? []).map((d) => ({ ts: d.ts, label: d.version })))
  const x = $derived((s?.points ?? []).map((p) => p.ts))
  const col = (name: string) => (s?.points ?? []).map((p) => p.c?.[name] ?? 0)
  const sources = $derived.by(() => {
    const t = s?.totals ?? {}
    return Object.entries(t)
      .filter(([k]) => k.startsWith('src_'))
      .sort((a, b) => b[1] - a[1])
      .slice(0, 5)
      .map(([k]) => k.slice(4))
  })
  // Live: per-second updates of this bot from the shared stream.
  const liveSeries = $derived(l?.history ?? [])
  const liveX = $derived(liveSeries.map((_, i) => Math.floor(Date.now() / 1000) - liveSeries.length + i))
</script>

<div class="tiles">
  <Tile label="запуск" value={bot.status?.started ? new Date(bot.status.started).toISOString().slice(5, 16).replace('T', ' ') : '—'} />
  <Tile label="аптайм" value={bot.status?.running ? uptime(bot.status.started) : '—'} />
  <Tile label="рестарты" value={bot.status?.restarts ?? 0} bad={(bot.status?.restarts ?? 0) > 0} />
  <Tile label="CPU / RAM" value={l ? `${l.cpu.toFixed(0)}% · ${bytes(l.rss)}` : '—'} />
  <Tile label="в обработке" value={l?.inflight ?? 0} />
  <Tile label="апд/с" value={l ? l.ups.toFixed(1) : '0'} />
  <Tile label="польз. / DAU" value={`${compact(u?.total ?? 0)} / ${compact(day?.series.uniques.dau ?? 0)}`} />
  <Tile label="новые / ушли" value={`${s?.totals.users_new ?? 0} / ${s?.totals.users_blocked ?? 0}`} />
  <Tile label="удержание D7" value={u ? Math.round((u.d7 ?? 0) * 100) + '%' : '—'} />
  <Tile label="доход 24ч" value={revenue} />
  <Tile label="ошибки" value={errors} bad={errors > 0} />
  <Tile label="p95" value={s ? Math.round(s.p95) + ' мс' : '—'} />
</div>

<div class="periods">
  {#each periods as p}
    <button class:active={period === p} onclick={() => (period = p)}>{labels[p]}</button>
  {/each}
  {#if error}<span class="danger small">{error}</span>{/if}
</div>

<div class="charts">
  {#if period === 'live'}
    <div class="chart"><h2>Апдейты в секунду</h2><Chart x={liveX} series={[{ label: 'апд/с', values: liveSeries }]} /></div>
  {:else if s}
    <div class="chart">
      <h2>Апдейты</h2>
      <Chart {x} {marks} series={[{ label: 'апдейты', values: col('updates') }, { label: 'ошибки', values: col('errors'), color: '#c8102e' }]} />
    </div>
    <div class="chart">
      <h2>Время ответа</h2>
      <Chart {x} {marks} unit=" мс"
        series={[
          { label: 'p50', values: s.points.map((p) => p.p50 ?? null) },
          { label: 'p95', values: s.points.map((p) => p.p95 ?? null) },
          { label: 'p99', values: s.points.map((p) => p.p99 ?? null) },
        ]} />
    </div>
    <div class="chart">
      <h2>Пользователи</h2>
      <Chart {x} {marks} series={[{ label: 'новые', values: col('users_new') }, { label: 'заблокировали', values: col('users_blocked'), color: '#c8102e' }, { label: 'вернулись', values: col('users_unblocked') }]} />
    </div>
    {#if sources.length}
      <div class="chart">
        <h2>Источники</h2>
        <Chart {x} series={sources.map((src) => ({ label: src, values: col('src_' + src) }))} />
      </div>
    {/if}
    {#if sum('pay_', '_count', s.totals) > 0}
      <div class="chart">
        <h2>Доход</h2>
        <Chart {x} {marks}
          series={Object.keys(s.totals).filter((k) => k.startsWith('pay_') && k.endsWith('_sum')).map((k) => ({ label: k.slice(4, -4), values: col(k) }))} />
      </div>
    {/if}
  {/if}
</div>

<style>
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
    gap: var(--s2);
    margin-bottom: var(--s3);
  }
  .periods {
    display: flex;
    gap: 0;
    align-items: center;
    margin-bottom: var(--s3);
  }
  .periods button + button {
    border-left: none;
  }
  .periods button.active {
    border-color: var(--accent);
    background: var(--surface-2);
  }
  .small {
    font-size: var(--fs-s);
    margin-left: var(--s3);
  }
  .charts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(420px, 1fr));
    gap: var(--s3);
    margin-bottom: var(--s4);
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
</style>
