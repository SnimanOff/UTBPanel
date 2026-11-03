<script lang="ts">
  // Live log stream with a level filter, pause of auto-scroll, server-side
  // search and download.
  import { onDestroy, tick } from 'svelte'
  import { api, errorText, subscribe, type LogLine } from '../../lib/api/client'
  import { utc } from '../../lib/format'

  let { botId }: { botId: number } = $props()
  const MAX = 3000
  let lines = $state<LogLine[]>([])
  let level = $state('')
  let follow = $state(true)
  let connected = $state(false)
  let query = $state('')
  let results = $state<LogLine[] | null>(null)
  let searching = $state(false)
  let error = $state('')
  let box: HTMLDivElement
  // Counters of the bot's log patterns for the last 24 h.
  let patterns = $state<[string, number][]>([])
  $effect(() => {
    void botId
    api
      .get<{ series: { totals: Record<string, number> } }>(`/api/bots/${botId}/metrics?period=24h`)
      .then((m) => (patterns = Object.entries(m.series.totals).filter(([k]) => k.startsWith('log_')).map(([k, v]) => [k.slice(4), v])), () => {})
  })
  let stop: (() => void) | undefined

  function connect() {
    stop?.()
    lines = []
    const q = new URLSearchParams({ limit: '500' })
    if (level) q.set('level', level)
    stop = subscribe(
      `/api/bots/${botId}/logs/stream?${q}`,
      {
        line: (l: LogLine) => {
          lines.push(l)
          if (lines.length > MAX) lines.splice(0, lines.length - MAX)
          if (follow) tick().then(() => box && (box.scrollTop = box.scrollHeight))
        },
      },
      (open) => (connected = open),
    )
  }

  $effect(() => {
    void botId
    void level
    connect()
  })
  onDestroy(() => stop?.())

  function onscroll() {
    const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 30
    if (!atBottom && follow) follow = false
  }

  async function search(e: SubmitEvent) {
    e.preventDefault()
    if (!query.trim()) {
      results = null
      return
    }
    searching = true
    error = ''
    try {
      const q = new URLSearchParams({ search: '1', q: query, limit: '1000' })
      if (level) q.set('level', level)
      results = await api.get<LogLine[]>(`/api/bots/${botId}/logs?${q}`)
    } catch (err) {
      error = errorText(err)
    } finally {
      searching = false
    }
  }

  const shown = $derived(results ?? lines)
</script>

<div class="bar">
  <select bind:value={level} title="Уровень">
    <option value="">все уровни</option>
    <option value="info">info+</option>
    <option value="warn">warn+</option>
    <option value="error">error</option>
  </select>
  <form onsubmit={search}>
    <input bind:value={query} placeholder="поиск по файлам логов" />
    <button disabled={searching}>Найти</button>
    {#if results}<button type="button" onclick={() => ((results = null), (query = ''))}>× сбросить</button>{/if}
  </form>
  <span class="grow"></span>
  <span class="muted small">{results ? `найдено ${results.length}` : connected ? 'поток' : 'нет связи'}</span>
  <button
    class:active={follow}
    onclick={() => {
      follow = !follow
      if (follow) box.scrollTop = box.scrollHeight
    }}>{follow ? '⏸ прокрутка' : '▶ прокрутка'}</button
  >
  <a class="button" href="/api/bots/{botId}/logs/download" download>Скачать</a>
</div>
{#if patterns.length}
  <div class="patterns muted small">
    за сутки: {#each patterns as [name, n]}<span class="pat"><span>{name}</span> <span class="num">{n}</span></span>{/each}
  </div>
{/if}
{#if error}<p class="danger">{error}</p>{/if}
<div class="log mono" bind:this={box} {onscroll}>
  {#each shown as l (l.seq + l.t)}
    <div class="line {l.l} {l.s}">
      <span class="t">{utc(l.t, true).slice(11)}</span>
      <span class="s">{l.s === 'core' ? '[core]' : l.s === 'stderr' ? 'err' : ''}</span>
      <span class="m">{l.m}</span>
    </div>
  {:else}
    <div class="muted empty">{results ? 'Ничего не найдено' : 'Строк пока нет'}</div>
  {/each}
</div>

<style>
  .patterns {
    display: flex;
    gap: var(--s3);
    margin-bottom: var(--s1);
  }
  .pat .num {
    color: var(--text);
  }
  .bar {
    display: flex;
    gap: var(--s2);
    align-items: center;
    margin-bottom: var(--s2);
    flex-wrap: wrap;
  }
  form {
    display: flex;
    gap: var(--s2);
  }
  form input {
    width: 260px;
  }
  .grow {
    flex: 1;
  }
  .small {
    font-size: var(--fs-s);
  }
  .button {
    border: var(--line);
    padding: var(--s1) var(--s3);
    background: var(--surface);
  }
  button.active {
    border-color: var(--accent);
  }
  .log {
    height: calc(100vh - 340px);
    min-height: 300px;
    overflow-y: auto;
    background: var(--bg);
    border: var(--line);
    font-size: var(--fs-s);
    padding: var(--s1) 0;
  }
  .line {
    display: grid;
    grid-template-columns: 96px 48px 1fr;
    padding: 0 var(--s2);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .line:hover {
    background: var(--surface);
  }
  .t,
  .s {
    color: var(--muted);
  }
  .line.warn .m {
    color: var(--warn);
  }
  .line.error .m {
    color: var(--danger-text);
  }
  .line.core .m {
    color: var(--accent-text);
  }
  .empty {
    padding: var(--s3);
  }
</style>
