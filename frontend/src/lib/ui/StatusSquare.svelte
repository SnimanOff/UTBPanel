<script lang="ts">
  // Bot status as a square: violet — running, hollow — paused,
  // grey — stopped, red — error.
  let { state, size = 10 }: { state: string; size?: number } = $props()
  const titles: Record<string, string> = {
    running: 'работает',
    paused: 'пауза',
    stopped: 'остановлен',
    crashed: 'упал',
    error: 'ошибка',
  }
</script>

<span class="sq {state}" style="--size: {size}px" title={titles[state] ?? state}></span>

<style>
  .sq {
    display: inline-block;
    width: var(--size);
    height: var(--size);
    flex: none;
    background: var(--muted);
    border: 1px solid var(--muted);
  }
  .running {
    background: var(--accent-hi);
    border-color: var(--accent-hi);
  }
  .paused {
    background: transparent;
    border-color: var(--accent-hi);
  }
  .crashed,
  .error {
    background: var(--danger);
    border-color: var(--danger);
  }
</style>
