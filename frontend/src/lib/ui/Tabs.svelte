<script lang="ts">
  // All tabs in one row; every tab opens with one click. On a narrow screen
  // the row scrolls sideways and the open tab is kept in view.
  let {
    tabs,
    active,
    onselect,
  }: { tabs: { id: string; label: string; hidden?: boolean }[]; active: string; onselect: (id: string) => void } = $props()

  let row = $state<HTMLDivElement>()
  const shown = $derived(tabs.filter((t) => !t.hidden))
  // Roving tabindex: exactly one tab stays in the tab order, even when the
  // open id is not one of them.
  const focusable = $derived(shown.some((t) => t.id === active) ? active : shown[0]?.id)

  // The open tab has to stay reachable: after a switch, and after font loading
  // or a badge changes the width of the row.
  function reveal() {
    if (!row || row.scrollWidth <= row.clientWidth) return
    const selected = row.querySelector<HTMLElement>('[aria-selected="true"]')
    if (!selected) return
    const { left, right } = selected.getBoundingClientRect()
    const { left: start, right: end } = row.getBoundingClientRect()
    if (left < start) row.scrollBy({ left: left - start })
    else if (right > end) row.scrollBy({ left: right - end })
  }

  $effect(() => {
    void active
    void shown
    reveal()
  })

  $effect(() => {
    if (!row || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(reveal)
    observer.observe(row)
    return () => observer.disconnect()
  })

  // Left and right arrows walk the row, as a tablist should; Tab enters the
  // list once and leaves it.
  function walk(e: KeyboardEvent) {
    const by = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0
    if (!by) return
    e.preventDefault()
    const next = shown[(shown.findIndex((t) => t.id === focusable) + by + shown.length) % shown.length]
    if (!next) return
    onselect(next.id)
    row?.querySelector<HTMLElement>(`#tab-${next.id}`)?.focus({ preventScroll: true })
  }
</script>

<div class="tabs" role="tablist" aria-label="Разделы бота" bind:this={row}>
  {#each shown as t}
    <button
      role="tab"
      id={`tab-${t.id}`}
      aria-selected={t.id === active}
      aria-controls="tab-panel"
      tabindex={t.id === focusable ? 0 : -1}
      class:active={t.id === active}
      onclick={() => onselect(t.id)}
      onkeydown={walk}
    >
      {t.label}
    </button>
  {/each}
</div>

<style>
  .tabs {
    display: flex;
    border-bottom: var(--line);
    overflow-x: auto;
    scrollbar-width: thin;
  }
  button {
    /* Never narrower than the label: on phones base.css gives buttons
       min-width: 40px, which lets flex items shrink and the labels run
       into each other. */
    flex: none;
    position: relative;
    border: none;
    border-bottom: 2px solid transparent;
    background: none;
    color: var(--muted);
    padding: var(--s2) var(--s3);
    white-space: nowrap;
  }
  button:hover {
    color: var(--text);
    border-color: transparent;
  }
  button.active {
    color: var(--text);
  }
  button:focus-visible {
    outline-offset: -2px;
  }
  button::after {
    content: '';
    position: absolute;
    inset: auto 0 0;
    height: 2px;
    background: var(--accent-hi);
    opacity: 0;
    transform: scaleX(0.55);
    transition: opacity var(--motion-fast) var(--motion-ease), transform var(--motion-fast) var(--motion-ease);
  }
  button:hover::after {
    opacity: 0.4;
    transform: scaleX(1);
  }
  button.active::after {
    opacity: 1;
    transform: scaleX(1);
  }
  @media (max-width: 700px) {
    button {
      padding: var(--s2);
    }
  }
</style>
