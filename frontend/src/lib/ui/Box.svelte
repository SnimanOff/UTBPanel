<script lang="ts">
  import type { Snippet } from 'svelte'
  let {
    title = '',
    children,
    actions,
    problem = false,
    pad = true,
  }: { title?: string; children: Snippet; actions?: Snippet; problem?: boolean; pad?: boolean } = $props()
</script>

<section class="box" class:problem class:pad>
  {#if title || actions}
    <header>
      {#if title}<h2>{title}</h2>{/if}
      {#if actions}<div class="actions">{@render actions()}</div>{/if}
    </header>
  {/if}
  {@render children()}
</section>

<style>
  .box {
    background: var(--surface);
    border: var(--line);
  }
  .box.pad {
    padding: var(--s3) var(--s4);
  }
  .problem {
    border-color: var(--danger);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--s3);
    margin-bottom: var(--s3);
  }
  .actions {
    display: flex;
    gap: var(--s2);
  }
</style>
