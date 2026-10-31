<script lang="ts">
  // Dangerous actions are confirmed by holding the button (~0.8 s): it fills
  // from left to right, no pop-up windows. Releasing or leaving cancels.
  // Space or Enter held down works the same.
  import type { Snippet } from 'svelte'

  let {
    onconfirm,
    children,
    danger = true,
    disabled = false,
    title = '',
    ms = 800,
  }: {
    onconfirm: () => void | Promise<void>
    children: Snippet
    danger?: boolean
    disabled?: boolean
    title?: string
    ms?: number
  } = $props()

  let progress = $state(0)
  let busy = $state(false)
  let raf = 0
  let start = 0

  function tick(now: number) {
    progress = Math.min(1, (now - start) / ms)
    if (progress >= 1) {
      finish()
      return
    }
    raf = requestAnimationFrame(tick)
  }

  function begin() {
    if (disabled || busy || raf) return
    start = performance.now()
    raf = requestAnimationFrame(tick)
  }

  function cancel() {
    cancelAnimationFrame(raf)
    raf = 0
    if (!busy) progress = 0
  }

  async function finish() {
    cancelAnimationFrame(raf)
    raf = 0
    busy = true
    try {
      await onconfirm()
    } finally {
      busy = false
      progress = 0
    }
  }

  function keydown(e: KeyboardEvent) {
    if ((e.key === ' ' || e.key === 'Enter') && !e.repeat) {
      e.preventDefault()
      begin()
    }
  }

  function keyup(e: KeyboardEvent) {
    if (e.key === ' ' || e.key === 'Enter') cancel()
  }
</script>

<button
  class="hold"
  class:danger
  class:busy
  {disabled}
  title={title || 'Удерживайте, чтобы подтвердить'}
  onpointerdown={begin}
  onpointerup={cancel}
  onpointerleave={cancel}
  onpointercancel={cancel}
  onkeydown={keydown}
  onkeyup={keyup}
  onclick={(e) => e.preventDefault()}
  style="--p: {progress}"
>
  <span class="fill" aria-hidden="true"></span>
  <span class="label">{@render children()}</span>
</button>

<style>
  .hold {
    position: relative;
    overflow: hidden;
    user-select: none;
    touch-action: none;
  }
  .fill {
    position: absolute;
    inset: 0;
    transform-origin: left;
    transform: scaleX(var(--p));
    background: var(--accent);
    pointer-events: none;
  }
  .danger .fill {
    background: var(--danger);
  }
  .label {
    position: relative;
  }
  .busy {
    color: var(--muted);
  }
</style>
