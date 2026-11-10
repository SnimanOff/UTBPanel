<script lang="ts">
  // All tabs in one row; every tab opens with one click. On a narrow screen
  // the row scrolls sideways and the open tab is kept in view.
  let {
    tabs,
    active,
    onselect,
  }: { tabs: { id: string; label: string; hidden?: boolean }[]; active: string; onselect: (id: string) => void } = $props()

  let row = $state<HTMLDivElement>()
  $effect(() => {
    void active
    row?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  })
</script>

<div class="tabs" role="tablist" bind:this={row}>
  {#each tabs.filter((t) => !t.hidden) as t}
    <button role="tab" aria-selected={t.id === active} class:active={t.id === active} onclick={() => onselect(t.id)}>
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
    border-bottom-color: var(--border);
  }
  button.active {
    color: var(--text);
    border-bottom-color: var(--accent-hi);
  }
  @media (max-width: 700px) {
    button {
      padding: var(--s2);
    }
  }
</style>
