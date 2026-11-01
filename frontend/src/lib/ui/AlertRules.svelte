<script lang="ts">
  // Threshold rules of one bot, or the common ones (botId 0, "Сервер").
  import { api, errorText, type Schemas } from '../api/client'
  import Box from './Box.svelte'

  let { botId = 0 }: { botId?: number } = $props()
  type R = Schemas['AlertRule']
  const names: Record<string, string> = {
    p95: 'p95 времени ответа, мс',
    err_rate: 'доля ошибок, %',
    cpu: 'CPU, %',
    rss: 'RAM, МБ',
    updates: 'апдейтов за период',
    silence: 'тишина (ни одного апдейта)',
    revenue_drop: 'падение дохода за сутки, %',
  }
  let rules = $state<R[]>([])
  let metric = $state<R['metric']>('p95')
  let op = $state<'>' | '<'>('>')
  let value = $state(2000)
  let minutes = $state(5)
  let error = $state('')

  async function load() {
    try {
      rules = await api.get<R[]>(`/api/alert-rules?bot=${botId}`)
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void botId
    load()
  })

  async function add(e: SubmitEvent) {
    e.preventDefault()
    error = ''
    try {
      await api.post('/api/alert-rules', { bot_id: botId, metric, op, value, minutes, enabled: true })
      load()
    } catch (err) {
      error = errorText(err)
    }
  }
  async function toggle(r: R) {
    await api.post('/api/alert-rules', { ...r, enabled: !r.enabled }).catch((e) => (error = errorText(e)))
    load()
  }
  async function remove(r: R) {
    await api.del(`/api/alert-rules/${r.id}`).catch((e) => (error = errorText(e)))
    load()
  }
  const describe = (r: R) =>
    r.metric === 'silence'
      ? `ни одного апдейта ${r.minutes} мин`
      : r.metric === 'revenue_drop'
        ? `доход за сутки упал больше чем на ${r.value}%`
        : `${names[r.metric]} ${r.op} ${r.value} за ${r.minutes} мин`
</script>

<Box title={botId ? 'Пороги' : 'Пороги для всех ботов'}>
  <ul>
    {#each rules as r (r.id)}
      <li class:off={!r.enabled}>
        <label><input type="checkbox" checked={r.enabled} onchange={() => toggle(r)} /> {describe(r)}</label>
        <button class="x" title="Удалить" onclick={() => remove(r)}>×</button>
      </li>
    {:else}
      <li class="muted">Правил нет</li>
    {/each}
  </ul>
  <form onsubmit={add}>
    <select bind:value={metric}>{#each Object.entries(names) as [k, v]}<option value={k}>{v}</option>{/each}</select>
    {#if metric !== 'silence'}
      {#if metric !== 'revenue_drop'}
        <select bind:value={op} class="op"><option>&gt;</option><option>&lt;</option></select>
      {/if}
      <input class="n" type="number" step="any" bind:value={value} />
    {/if}
    {#if metric !== 'revenue_drop'}за <input class="n" type="number" min="1" bind:value={minutes} /> мин{/if}
    <button>Добавить</button>
  </form>
  <p class="muted">Повтор — не чаще раза в 30 минут; когда метрика вернётся в норму, придёт отдельное уведомление.</p>
  {#if error}<p class="danger">{error}</p>{/if}
</Box>

<style>
  ul {
    list-style: none;
    padding: 0;
    margin: 0 0 var(--s2);
  }
  li {
    display: flex;
    align-items: center;
    padding: 2px 0;
    border-bottom: var(--line);
  }
  li label {
    flex: 1;
  }
  li.off {
    opacity: 0.6;
  }
  form {
    display: flex;
    gap: var(--s2);
    align-items: center;
    flex-wrap: wrap;
  }
  .n {
    width: 6em;
  }
  .op {
    width: 3.5em;
  }
  .x {
    border: none;
    background: none;
  }
</style>
