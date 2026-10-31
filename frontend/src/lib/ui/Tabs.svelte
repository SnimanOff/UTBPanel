<script lang="ts">
  // All tabs in one row; every tab opens with one click.
  let {
    tabs,
    active,
    onselect,
  }: { tabs: { id: string; label: string; hidden?: boolean }[]; active: string; onselect: (id: string) => void } = $props()
</script>

<div class="tabs" role="tablist">
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
  }
  button {
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
</style>
