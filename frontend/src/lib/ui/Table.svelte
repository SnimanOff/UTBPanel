<script lang="ts" generics="T">
  // A table with sorting by column and pages.
  import type { Snippet } from 'svelte'

  type Col = { key: string; label: string; sort?: (row: T) => string | number; align?: 'right' }
  let {
    rows,
    cols,
    cell,
    pageSize = 50,
    empty = 'Пусто',
    onrow,
  }: {
    rows: T[]
    cols: Col[]
    cell: Snippet<[T, string]>
    pageSize?: number
    empty?: string
    onrow?: (row: T) => void
  } = $props()

  let sortKey = $state('')
  let desc = $state(false)
  let page = $state(0)

  const sorted = $derived.by(() => {
    const col = cols.find((c) => c.key === sortKey)
    if (!col?.sort) return rows
    const f = col.sort
    return [...rows].sort((a, b) => {
      const x = f(a), y = f(b)
      return (x < y ? -1 : x > y ? 1 : 0) * (desc ? -1 : 1)
    })
  })
  const pages = $derived(Math.max(1, Math.ceil(sorted.length / pageSize)))
  const shown = $derived(sorted.slice(page * pageSize, (page + 1) * pageSize))

  $effect(() => {
    if (page >= pages) page = pages - 1
  })

  function sortBy(c: Col) {
    if (!c.sort) return
    if (sortKey === c.key) desc = !desc
    else {
      sortKey = c.key
      desc = false
    }
  }
</script>

<table>
  <thead>
    <tr>
      {#each cols as c}
        <th class:sortable={!!c.sort} class:right={c.align === 'right'} onclick={() => sortBy(c)}>
          {c.label}{#if sortKey === c.key}{desc ? ' ↓' : ' ↑'}{/if}
        </th>
      {/each}
    </tr>
  </thead>
  <tbody>
    {#each shown as row}
      <tr class:clickable={!!onrow} onclick={() => onrow?.(row)}>
        {#each cols as c}
          <td class:right={c.align === 'right'}>{@render cell(row, c.key)}</td>
        {/each}
      </tr>
    {:else}
      <tr><td colspan={cols.length} class="muted empty">{empty}</td></tr>
    {/each}
  </tbody>
</table>
{#if pages > 1}
  <div class="pager">
    <button disabled={page === 0} onclick={() => page--}>←</button>
    <span class="num muted">{page + 1} / {pages}</span>
    <button disabled={page >= pages - 1} onclick={() => page++}>→</button>
  </div>
{/if}

<style>
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
    white-space: nowrap;
  }
  .sortable {
    cursor: pointer;
  }
  .right {
    text-align: right;
  }
  tr.clickable {
    cursor: pointer;
  }
  tr.clickable:hover td {
    background: var(--surface-2);
  }
  .empty {
    padding: var(--s4);
  }
  .pager {
    display: flex;
    gap: var(--s2);
    align-items: center;
    margin-top: var(--s2);
  }
</style>
