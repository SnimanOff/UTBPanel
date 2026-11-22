<script lang="ts">
  // Dangerous actions are confirmed by holding the button (~0.8 s): it fills
  // from left to right, no pop-up windows. Releasing or leaving cancels.
  // Space or Enter held down works the same.
  import { onDestroy, type Snippet } from 'svelte'

  let {
    onconfirm,
    children,
    danger = true,
    disabled = false,
    title = '',
    label,
    ms = 800,
  }: {
    onconfirm: () => void | Promise<void>
    children: Snippet
    danger?: boolean
    disabled?: boolean
    title?: string
    label?: string
    ms?: number
  } = $props()

  let progress = $state(0)
  let busy = $state(false)
  let button: HTMLButtonElement
  let raf = 0
  let start = 0
  let pointerId: number | undefined
  let key: string | undefined

  onDestroy(cancel)
  $effect(() => {
    if (disabled) cancel()
  })

  function tick(now: number) {
    if (disabled || (key !== undefined && document.activeElement !== button)) {
      cancel()
      return
    }
    progress = Math.min(1, (now - start) / ms)
    if (progress >= 1) {
      finish()
      return
    }
    raf = requestAnimationFrame(tick)
  }

  function begin() {
    if (disabled || busy || raf) return false
    start = performance.now()
    raf = requestAnimationFrame(tick)
    window.addEventListener('blur', cancel)
    document.addEventListener('visibilitychange', cancel)
    return true
  }

  function cancel() {
    if (raf) {
      cancelAnimationFrame(raf)
      window.removeEventListener('blur', cancel)
      document.removeEventListener('visibilitychange', cancel)
    }
    raf = 0
    pointerId = undefined
    key = undefined
    if (!busy) progress = 0
  }

  async function finish() {
    busy = true
    cancel()
    try {
      await onconfirm()
    } finally {
      busy = false
      progress = 0
    }
  }

  function pointerdown(e: PointerEvent) {
    if (e.isPrimary && e.button === 0 && begin()) pointerId = e.pointerId
  }

  function endPointer(e: PointerEvent) {
    if (e.pointerId === pointerId) cancel()
  }

  function pointermove(e: PointerEvent) {
    if (e.pointerId !== pointerId) return
    // Touch pointers keep implicit capture even when the finger leaves the button.
    const { left, right, top, bottom } = (e.currentTarget as HTMLButtonElement).getBoundingClientRect()
    if (e.clientX < left || e.clientX > right || e.clientY < top || e.clientY > bottom) cancel()
  }

  function keydown(e: KeyboardEvent) {
    if (e.key !== ' ' && e.key !== 'Enter') return
    e.preventDefault()
    if (!e.repeat && begin()) key = e.key
  }

  function keyup(e: KeyboardEvent) {
    if (e.key !== key) return
    e.preventDefault()
    cancel()
  }
</script>

<button
  class="hold"
  class:danger
  class:busy
  type="button"
  bind:this={button}
  {disabled}
  aria-label={label}
  aria-busy={busy}
  title={title || 'Удерживайте, чтобы подтвердить'}
  onpointerdown={pointerdown}
  onpointermove={pointermove}
  onpointerup={endPointer}
  onpointerleave={endPointer}
  onpointercancel={endPointer}
  onlostpointercapture={endPointer}
  onkeydown={keydown}
  onkeyup={keyup}
  onblur={cancel}
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
    touch-action: manipulation;
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
