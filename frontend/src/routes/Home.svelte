<script lang="ts">
  // Main page: groups are horizontal separators with cards under them.
  // Filter by status on top works across groups; "/" focuses the search.
  // Cards are dragged between groups; groups are created, renamed, deleted
  // and reordered right here.
  import { api, errorText, type BotView, type Group } from '../lib/api/client'
  import { pollBots, store } from '../lib/api/stores.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'
  import BotCard from './BotCard.svelte'
  import Export from './bot/Export.svelte'

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
    return act(() => Promise.allSettled(ids.map((id) => api.post(`/api/bots/${id}/${op}`))))
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
      <button class:active={filter === f.id} onclick={() => (filter = f.id)}>{f.label}</button>
    {/each}
  </div>
  <input class="search" bind:this={search} bind:value={query} placeholder="поиск: /" />
</div>

{#if error}<p class="danger">{error}</p>{/if}
{#if store.bots.length}<Export all />{/if}
{#if store.error}<p class="danger">{store.error}</p>{/if}

{#each sections as s (s.group.id)}
  {#if s.group.id !== 0 || s.all.length > 0 || store.groups.length === 0}
    <section
      class="group"
      role="list"
      ondragover={(e) => e.preventDefault()}
      ondrop={(e) => {
        e.preventDefault()
        drop(s.group.id)
      }}
    >
      <div class="sep">
        <button class="collapse" onclick={() => (collapsed[s.group.id] = !collapsed[s.group.id])} title="Свернуть">
          {collapsed[s.group.id] ? '+' : '−'}
        </button>
        <span class="name">{s.group.name}</span>
        <span class="muted num">{s.all.filter((b) => b.state === 'running').length} из {s.all.length} работают</span>
        <span class="line"></span>
        {#if s.group.id !== 0}
          <button title="Выше" onclick={() => moveGroup(s.group.id, -1)}>↑</button>
          <button title="Ниже" onclick={() => moveGroup(s.group.id, 1)}>↓</button>
          <button title="Переименовать" onclick={() => renameGroup(s.group as Group)}>✎</button>
        {/if}
        {#if s.all.length > 0}
          <button onclick={() => massAction(s.all.map((b) => b.id), 'start')}>▶ все</button>
          <HoldButton onconfirm={() => massAction(s.all.map((b) => b.id), 'stop')}>■ все</HoldButton>
        {/if}
        {#if s.group.id !== 0 && s.all.length === 0}
          <HoldButton onconfirm={() => act(() => api.del(`/api/groups/${s.group.id}`))}>удалить</HoldButton>
        {/if}
      </div>
      {#if !collapsed[s.group.id]}
        <div class="cards">
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
            <p class="muted empty">{s.all.length ? 'Нет ботов под фильтр' : 'Перетащите сюда карточку'}</p>
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
  .toolbar {
    display: flex;
    justify-content: space-between;
    gap: var(--s3);
    margin-bottom: var(--s4);
  }
  .filters {
    display: flex;
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
  .sep button {
    min-height: 24px;
    padding: 0 var(--s2);
  }
  .collapse {
    width: 24px;
  }
  .name {
    font-weight: 500;
  }
  .line {
    flex: 1;
    border-top: var(--line);
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: var(--s3);
  }
  .slot {
    cursor: grab;
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
</style>
