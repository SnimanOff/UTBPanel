<script lang="ts">
  import type { Snippet } from 'svelte'
  let { label, error = '', hint = '', children }: { label: string; error?: string; hint?: string; children: Snippet } = $props()
</script>

<label class="field">
  <span class="label">{label}</span>
  {@render children()}
  {#if error}<span class="error">{error}</span>{:else if hint}<span class="hint">{hint}</span>{/if}
</label>

<style>
  .field {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-bottom: var(--s3);
  }
  .label {
    color: var(--muted);
    font-size: var(--fs-s);
  }
  .error {
    color: var(--danger-text);
    font-size: var(--fs-s);
    white-space: pre-wrap;
  }
  .hint {
    color: var(--muted);
    font-size: var(--fs-xs);
  }
</style>
