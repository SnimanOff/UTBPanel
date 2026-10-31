<script lang="ts">
  let { label, value, hint = '', bad = false }: { label: string; value: string | number; hint?: string; bad?: boolean } = $props()
</script>

<div class="tile" class:bad>
  <div class="label">{label}</div>
  <div class="value num">{value}</div>
  {#if hint}<div class="hint">{hint}</div>{/if}
</div>

<style>
  .tile {
    border: var(--line);
    background: var(--surface);
    padding: var(--s2) var(--s3);
    min-width: 120px;
  }
  .label {
    color: var(--muted);
    font-size: var(--fs-xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .value {
    font-size: var(--fs-l);
    margin-top: 2px;
  }
  .hint {
    color: var(--muted);
    font-size: var(--fs-xs);
  }
  .bad .value {
    color: var(--danger);
  }
</style>
