<script lang="ts">
  // The notification feed under the bell.
  export type Item = { id: number; ts: string; text: string; severity: string; href?: string; read?: boolean }
  let { items, onopen }: { items: Item[]; onopen?: (i: Item) => void } = $props()

  function time(ts: string) {
    const d = new Date(ts)
    return d.toISOString().slice(5, 16).replace('T', ' ')
  }
</script>

<ul class="feed">
  {#each items as i (i.id)}
    <li class={i.severity} class:unread={!i.read}>
      <button onclick={() => onopen?.(i)}>
        <span class="t num muted">{time(i.ts)}</span>
        <span>{i.text}</span>
      </button>
    </li>
  {:else}
    <li class="muted empty">Событий нет</li>
  {/each}
</ul>

<style>
  .feed {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 420px;
    overflow-y: auto;
  }
  li {
    border-bottom: var(--line);
    border-left: 2px solid transparent;
  }
  li.error {
    border-left-color: var(--danger);
  }
  li.warn {
    border-left-color: var(--warn);
  }
  li.unread {
    background: var(--surface-2);
  }
  button {
    display: flex;
    gap: var(--s2);
    width: 100%;
    text-align: left;
    border: none;
    background: none;
    padding: var(--s2);
  }
  .t {
    flex: none;
    font-size: var(--fs-xs);
  }
  .empty {
    padding: var(--s3);
  }
</style>
