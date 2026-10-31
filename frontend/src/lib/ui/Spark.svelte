<script lang="ts">
  // A tiny live chart for cards: plain SVG, no library.
  let { values, width = 80, height = 18 }: { values: number[]; width?: number; height?: number } = $props()
  const points = $derived.by(() => {
    if (values.length < 2) return ''
    const max = Math.max(1, ...values)
    const step = width / (values.length - 1)
    return values.map((v, i) => `${(i * step).toFixed(1)},${(height - (v / max) * (height - 1) - 0.5).toFixed(1)}`).join(' ')
  })
</script>

<svg {width} {height} viewBox="0 0 {width} {height}" aria-hidden="true">
  <polyline {points} fill="none" stroke="var(--accent-hi)" stroke-width="1" />
</svg>
