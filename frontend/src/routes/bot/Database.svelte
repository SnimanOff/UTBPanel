<script lang="ts">
  // Database: tables with schema and rows, a page view, the SQL console
  // (read-only; writing is a switch and a held button), CSV export.
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import { bytes } from '../../lib/format'
  import HoldButton from '../../lib/ui/HoldButton.svelte'

  let { botId }: { botId: number } = $props()
  type Info = Schemas['DBInfo']
  type Res = Schemas['DBResult']
  const PAGE = 100
  let info = $state<Info>()
  let table = $state('')
  let page = $state(0)
  let data = $state<Res>()
  let sql = $state('SELECT * FROM sqlite_schema LIMIT 20')
  let write = $state(false)
  let result = $state<Res>()
  let error = $state('')
  let consoleError = $state('')

  async function loadInfo() {
    error = ''
    try {
      info = await api.get<Info>(`/api/bots/${botId}/db`)
      if (!table && info.tables.length) table = info.tables[0].name
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void botId
    loadInfo()
  })
  $effect(() => {
    if (!table) return
    api
      .get<Res>(`/api/bots/${botId}/db/tables/${encodeURIComponent(table)}?limit=${PAGE}&offset=${page * PAGE}`)
      .then((r) => (data = r), (e) => (error = errorText(e)))
  })

  async function run() {
    consoleError = ''
    try {
      result = await api.post<Res>(`/api/bots/${botId}/db/${write ? 'exec' : 'query'}`, { sql })
      if (write) loadInfo()
    } catch (e) {
      consoleError = errorText(e)
    }
  }

  const current = $derived(info?.tables.find((t) => t.name === table))
  const pages = $derived(Math.max(1, Math.ceil((current?.rows ?? 0) / PAGE)))
  const show = (v: unknown) => (v === null ? 'NULL' : String(v))
</script>

{#if error}<p class="danger">{error}</p>{/if}
{#if info}
  <div class="layout">
    <aside>
      <div class="muted small">БД {bytes(info.size)} · WAL {bytes(info.wal_size)}</div>
      {#each info.tables as t}
        <button class="table" class:active={t.name === table} onclick={() => ((table = t.name), (page = 0))}>
          <span>{t.name}</span><span class="num muted">{t.rows}</span>
        </button>
      {:else}
        <p class="muted">Таблиц нет</p>
      {/each}
    </aside>
    <section>
      {#if current}
        <div class="head">
          <span class="mono">{current.name}</span>
          <span class="muted small">{current.columns.map((c) => `${c.name} ${c.type}${c.pk ? ' PK' : ''}`).join(' · ')}</span>
          <span class="grow"></span>
          <a class="button" href="/api/bots/{botId}/db/export?table={encodeURIComponent(current.name)}" download>CSV</a>
        </div>
        {#if data}
          <div class="grid">
            <table class="mono">
              <thead><tr>{#each data.columns as c}<th>{c}</th>{/each}</tr></thead>
              <tbody>
                {#each data.rows as row}
                  <tr>{#each row as v}<td class:null={v === null}>{show(v)}</td>{/each}</tr>
                {/each}
              </tbody>
            </table>
          </div>
          {#if pages > 1}
            <div class="pager">
              <button disabled={page === 0} onclick={() => page--}>←</button>
              <span class="num muted">{page + 1} / {pages}</span>
              <button disabled={page >= pages - 1} onclick={() => page++}>→</button>
            </div>
          {/if}
        {/if}
      {/if}

      <h2 class="console-title">SQL-консоль</h2>
      <textarea bind:value={sql} rows="4" spellcheck="false"></textarea>
      <div class="run">
        <label><input type="checkbox" bind:checked={write} /> запись</label>
        {#if write}
          <HoldButton onconfirm={run}>Выполнить с записью</HoldButton>
          <span class="danger small">изменит базу работающего бота; текст запроса попадёт в аудит</span>
        {:else}
          <button class="primary" onclick={run}>Выполнить</button>
          <span class="muted small">только чтение</span>
          <a class="button" href="/api/bots/{botId}/db/export?sql={encodeURIComponent(sql)}" download>CSV</a>
        {/if}
      </div>
      {#if consoleError}<p class="danger pre">{consoleError}</p>{/if}
      {#if result}
        <div class="muted small">
          {result.affected !== undefined && result.columns.length === 0 ? `изменено строк: ${result.affected}` : `строк: ${result.rows.length}${result.truncated ? ' (обрезано)' : ''}`}
          · {result.ms.toFixed(1)} мс
        </div>
        {#if result.columns.length}
          <div class="grid">
            <table class="mono">
              <thead><tr>{#each result.columns as c}<th>{c}</th>{/each}</tr></thead>
              <tbody>{#each result.rows as row}<tr>{#each row as v}<td class:null={v === null}>{show(v)}</td>{/each}</tr>{/each}</tbody>
            </table>
          </div>
        {/if}
      {/if}
    </section>
  </div>
{/if}

<style>
  .layout {
    display: grid;
    grid-template-columns: 220px 1fr;
    gap: var(--s3);
  }
  aside {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .table {
    display: flex;
    justify-content: space-between;
    text-align: left;
    border: none;
    background: none;
  }
  .table.active {
    background: var(--surface-2);
    border-left: 2px solid var(--accent-hi);
  }
  .head {
    display: flex;
    gap: var(--s3);
    align-items: center;
    margin-bottom: var(--s2);
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
  .grid {
    overflow: auto;
    max-height: 420px;
    border: var(--line);
  }
  table {
    font-size: var(--fs-s);
  }
  th,
  td {
    text-align: left;
    padding: 2px var(--s2);
    border-bottom: var(--line);
    white-space: nowrap;
    max-width: 360px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  th {
    position: sticky;
    top: 0;
    background: var(--surface);
    color: var(--muted);
    font-weight: 400;
  }
  .null {
    color: var(--muted);
  }
  .pager {
    display: flex;
    gap: var(--s2);
    align-items: center;
    margin-top: var(--s2);
  }
  .console-title {
    margin: var(--s4) 0 var(--s2);
  }
  textarea {
    width: 100%;
  }
  .run {
    display: flex;
    gap: var(--s3);
    align-items: center;
    margin: var(--s2) 0;
  }
  .pre {
    white-space: pre-wrap;
  }
</style>
