<script lang="ts" module>
  /** Words for bot states: the square's label and the card's status line. */
  export const stateTitles: Record<string, string> = {
    running: 'работает',
    paused: 'пауза',
    stopped: 'остановлен',
    crashed: 'упал',
    error: 'ошибка',
  }
</script>

<script lang="ts">
  // Bot status as a square: violet — running, hollow — paused,
  // grey — stopped, red — error. The color is not the only sign: screen
  // readers get the word.
  let { state, size = 10 }: { state: string; size?: number } = $props()
  const title = $derived(stateTitles[state] ?? state)
</script>

<span class="sq {state}" style="--size: {size}px" role="img" aria-label={title} {title}></span>

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
