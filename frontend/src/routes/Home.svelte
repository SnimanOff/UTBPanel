<script lang="ts">
  // Main page: groups are horizontal separators with cards under them.
  // Filter by status on top works across groups; "/" focuses the search.
  // Cards are dragged between groups; groups are created, renamed, deleted
  // and reordered right here.
  import { api, ApiError, errorText, type BotView, type Group } from '../lib/api/client'
  import { pollBots, store } from '../lib/api/stores.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'
  import { fadeSlide } from '../lib/ui/motion'
  import BotCard from './BotCard.svelte'
  import Export from './bot/Export.svelte'
  import ImportBot from './ImportBot.svelte'

  type Filter = 'all' | 'running' | 'paused' | 'stopped' | 'error'
  const filters: { id: Filter; label: string }[] = [
    { id: 'all', label: 'все' },
    { id: 'running', label: 'работают' },
    { id: 'paused', label: 'пауза' },
    { id: 'stopped', label: 'остановлены' },
    { id: 'error', label: 'ошибки' },
  ]
  let filter = $state<Filter>('all')
  let query = $state('')
  let search: HTMLInputElement
  let collapsed = $state<Record<number, boolean>>(loadCollapsed())
  let error = $state('')
  let dragId = $state(0)
  let newGroup = $state('')

  $effect(() => pollBots())

  function loadCollapsed(): Record<number, boolean> {
    try {
      return JSON.parse(localStorage.getItem('collapsed') ?? '{}')
    } catch {
      return {}
    }
  }
  $effect(() => {
    try {
      localStorage.setItem('collapsed', JSON.stringify(collapsed))
    } catch {}
  })

  function matches(b: BotView): boolean {
    const st = b.state === 'crashed' ? 'error' : b.state
    if (filter !== 'all' && st !== filter) return false
    const q = query.trim().toLowerCase()
    return !q || b.name.toLowerCase().includes(q) || b.username.toLowerCase().includes(q)
  }

  const sections = $derived.by(() => {
    const gs: (Group | { id: 0; name: string })[] = [...store.groups, { id: 0, name: 'Без группы' }]
    return gs.map((g) => {
      const all = store.bots.filter((b) => (b.group_id ?? 0) === g.id).sort((a, b) => a.position - b.position)
      return { group: g, all, shown: all.filter(matches) }
    })
  })

  function onkey(e: KeyboardEvent) {
    if (e.key === '/' && document.activeElement?.tagName !== 'INPUT') {
      e.preventDefault()
      search.focus()
    }
  }

  async function act(fn: () => Promise<unknown>) {
    error = ''
    try {
      await fn()
    } catch (e) {
      error = errorText(e)
    }
    await store.load()
  }

  function massAction(ids: number[], op: 'start' | 'stop') {
    return act(async () => {
      const results = await Promise.allSettled(ids.map((id) => api.post(`/api/bots/${id}/${op}`)))
      const failed = results.filter((result): result is PromiseRejectedResult => {
        if (result.status !== 'rejected') return false
        // A start conflict means the bot is already running.
        return !(op === 'start' && result.reason instanceof ApiError && result.reason.code === 'conflict')
      })
      if (failed.length) {
        error = `Действие не выполнено для ${failed.length} из ${ids.length} ботов: ${errorText(failed[0].reason)}`
      }
    })
  }

  async function drop(groupId: number, beforeId?: number) {
    const id = dragId
    dragId = 0
    if (!id) return
    const ids = store.bots.filter((b) => (b.group_id ?? 0) === groupId && b.id !== id).sort((a, b) => a.position - b.position).map((b) => b.id)
    const at = beforeId ? ids.indexOf(beforeId) : -1
    ids.splice(at >= 0 ? at : ids.length, 0, id)
    await act(() => api.put('/api/bots/order', { group_id: groupId, ids }))
  }

  async function moveGroup(id: number, dir: -1 | 1) {
    const ids = store.groups.map((g) => g.id)
    const i = ids.indexOf(id)
    const j = i + dir
    if (j < 0 || j >= ids.length) return
    ;[ids[i], ids[j]] = [ids[j], ids[i]]
    await act(() => api.put('/api/groups/order', { ids }))
  }

  async function renameGroup(g: Group) {
    const name = prompt('Новое имя группы', g.name)
    if (name && name !== g.name) await act(() => api.patch(`/api/groups/${g.id}`, { name }))
  }

  async function addGroup(e: SubmitEvent) {
    e.preventDefault()
    if (!newGroup.trim()) return
    await act(() => api.post('/api/groups', { name: newGroup.trim() }))
    newGroup = ''
  }
</script>

<svelte:window onkeydown={onkey} />

<div class="toolbar">
  <div class="filters">
    {#each filters as f}
      <button class:active={filter === f.id} aria-pressed={filter === f.id} onclick={() => (filter = f.id)}>{f.label}</button>
    {/each}
  </div>
  <input class="search" aria-label="Поиск ботов" bind:this={search} bind:value={query} placeholder="поиск: /" />
</div>

{#if error}<p class="danger">{error}</p>{/if}
<div class="transfer">
  {#if store.bots.length}<Export all />{/if}
  <ImportBot />
</div>
{#if store.error}<p class="danger">{store.error}</p>{/if}

{#each sections as s (s.group.id)}
  {#if s.group.id !== 0 || s.all.length > 0 || store.groups.length === 0}
    <section
      class="group"
      role="group"
      aria-label={s.group.name}
      ondragover={(e) => e.preventDefault()}
      ondrop={(e) => {
        e.preventDefault()
        drop(s.group.id)
      }}
    >
      <div class="sep">
        <button
          class="collapse"
          aria-expanded={!collapsed[s.group.id]}
          aria-label={`${collapsed[s.group.id] ? 'Развернуть' : 'Свернуть'} группу ${s.group.name}`}
          onclick={() => (collapsed[s.group.id] = !collapsed[s.group.id])}
          title={collapsed[s.group.id] ? 'Развернуть' : 'Свернуть'}
        >
          <span aria-hidden="true">{collapsed[s.group.id] ? '+' : '−'}</span>
        </button>
        <span class="name">{s.group.name}</span>
        <span class="muted num">{s.all.filter((b) => b.state === 'running').length} из {s.all.length} работают</span>
        <span class="line"></span>
        <div class="group-actions">
          {#if s.group.id !== 0}
            <button title="Выше" aria-label="Выше" onclick={() => moveGroup(s.group.id, -1)}>↑</button>
            <button title="Ниже" aria-label="Ниже" onclick={() => moveGroup(s.group.id, 1)}>↓</button>
            <button title="Переименовать" aria-label="Переименовать группу" onclick={() => renameGroup(s.group as Group)}>✎</button>
          {/if}
          {#if s.all.length > 0}
            <button aria-label="Запустить всех ботов группы" onclick={() => massAction(s.all.map((b) => b.id), 'start')}>▶ все</button>
            <HoldButton label="Остановить всех ботов группы — удерживайте" onconfirm={() => massAction(s.all.map((b) => b.id), 'stop')}>■ все</HoldButton>
          {/if}
          {#if s.group.id !== 0 && s.all.length === 0}
            <HoldButton onconfirm={() => act(() => api.del(`/api/groups/${s.group.id}`))}>удалить</HoldButton>
          {/if}
        </div>
      </div>
      {#if !collapsed[s.group.id]}
        <div class="cards" role="list" in:fadeSlide>
          {#each s.shown as b (b.id)}
            <div
              class="slot"
              role="listitem"
              draggable="true"
              ondragstart={() => (dragId = b.id)}
              ondragover={(e) => e.preventDefault()}
              ondrop={(e) => {
                e.preventDefault()
                e.stopPropagation()
                drop(s.group.id, b.id)
              }}
            >
              <BotCard bot={b} />
            </div>
          {:else}
            <p class="muted empty" role="presentation">{s.all.length ? 'Нет ботов под фильтр' : 'Перетащите сюда карточку'}</p>
          {/each}
        </div>
      {/if}
    </section>
  {/if}
{/each}

<form class="newgroup" onsubmit={addGroup}>
  <input bind:value={newGroup} placeholder="Новая группа" maxlength="64" />
  <button disabled={!newGroup.trim()}>Создать</button>
</form>

{#if store.loaded && store.bots.length === 0 && !store.error}
  <p class="muted">Ботов пока нет. Нажмите «+ Бот» вверху справа.</p>
{/if}

<style>
  /* Export and import side by side; an opened form takes the whole row. */
  .transfer {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s2);
    align-items: flex-start;
  }
  .transfer > :global(:has(.box)) {
    flex-basis: 100%;
  }
  .toolbar {
    display: flex;
    justify-content: space-between;
    gap: var(--s3);
    margin-bottom: var(--s4);
  }
  .filters {
    display: flex;
  }
  .filters button {
    white-space: nowrap;
  }
  .filters button + button {
    border-left: none;
  }
  .filters button.active {
    border-color: var(--accent);
    background: var(--surface-2);
  }
  .search {
    width: 240px;
  }
  .group {
    margin-bottom: var(--s5);
  }
  .sep {
    display: flex;
    align-items: center;
    gap: var(--s2);
    margin-bottom: var(--s3);
  }
  .group-actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s2);
  }
  .collapse,
  .group-actions :global(button) {
    min-width: 24px;
    padding: 0 var(--s2);
    white-space: nowrap;
  }
  .collapse {
    flex: none;
  }
  .name {
    font-weight: 500;
    overflow-wrap: anywhere;
  }
  .line {
    flex: 1;
    border-top: var(--line);
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(300px, 100%), 1fr));
    gap: var(--s3);
  }
  .slot {
    cursor: grab;
    display: grid;
    min-width: 0;
  }
  .empty {
    grid-column: 1 / -1;
    border: 1px dashed var(--border);
    padding: var(--s3);
  }
  .newgroup {
    display: flex;
    gap: var(--s2);
    max-width: 360px;
  }
  .newgroup input {
    min-width: 0;
  }

  /* Phones: one column, rows of controls wrap. */
  @media (max-width: 700px) {
    .collapse,
    .group-actions :global(button) {
      min-width: 40px;
    }
    .sep {
      flex-wrap: wrap;
    }
    .group-actions {
      flex-basis: 100%;
    }
    .group-actions:empty {
      display: none;
    }
    .toolbar {
      flex-wrap: wrap;
    }
    .filters {
      flex-wrap: wrap;
      gap: var(--s1);
    }
    .filters button + button {
      border-left: var(--line);
    }
    .filters button.active {
      border-left-color: var(--accent);
    }
  }
</style>
