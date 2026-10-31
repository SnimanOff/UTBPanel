<script lang="ts">
  // A line chart on uPlot, loaded on demand so pages without charts do not
  // pay for it. Deploy marks are vertical lines with the version.
  import { onDestroy } from 'svelte'
  import type uPlotType from 'uplot'

  type Series = { label: string; color?: string; values: (number | null)[] }
  let {
    x,
    series,
    height = 160,
    marks = [],
    unit = '',
  }: {
    x: number[] // unix seconds
    series: Series[]
    height?: number
    marks?: { ts: number; label: string }[]
    unit?: string
  } = $props()

  let el: HTMLDivElement
  let plot: uPlotType | undefined
  let width = $state(600)
  const palette = ['#8a45c8', '#e9e4ee', '#b8862b', '#4f9d8f', '#c8102e']

  function opts(U: typeof uPlotType): uPlotType.Options {
    const axis = { stroke: '#8b8195', grid: { stroke: '#2b2236', width: 1 }, ticks: { stroke: '#2b2236' }, font: '11px JetBrains Mono' }
    return {
      width,
      height,
      legend: { show: series.length > 1 },
      cursor: { points: { size: 5 } },
      scales: { x: { time: true } },
      axes: [axis, { ...axis, values: (_u: uPlotType, v: number[]) => v.map((n) => fmt(n) + unit) }],
      series: [
        {},
        ...series.map((s, i) => ({ label: s.label, stroke: s.color ?? palette[i % palette.length], width: 1, points: { show: false } })),
      ],
      hooks: {
        draw: [
          (u: uPlotType) => {
            const ctx = u.ctx
            ctx.save()
            ctx.strokeStyle = '#6b2fa3'
            ctx.fillStyle = '#8b8195'
            ctx.font = '10px JetBrains Mono'
            for (const m of marks) {
              const px = u.valToPos(m.ts, 'x', true)
              if (px < u.bbox.left || px > u.bbox.left + u.bbox.width) continue
              ctx.beginPath()
              ctx.moveTo(px, u.bbox.top)
              ctx.lineTo(px, u.bbox.top + u.bbox.height)
              ctx.stroke()
              ctx.fillText(m.label, px + 3, u.bbox.top + 10)
            }
            ctx.restore()
          },
        ],
      },
    }
  }

  function fmt(n: number): string {
    if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(1) + 'M'
    if (Math.abs(n) >= 1e3) return (n / 1e3).toFixed(1) + 'k'
    return Number.isInteger(n) ? String(n) : n.toFixed(2)
  }

  $effect(() => {
    const data = [x, ...series.map((s) => s.values)] as uPlotType.AlignedData
    void marks
    import('uplot').then(({ default: U }) => {
      import('uplot/dist/uPlot.min.css')
      plot?.destroy()
      plot = new U(opts(U), data, el)
    })
  })

  $effect(() => {
    plot?.setSize({ width, height })
  })

  onDestroy(() => plot?.destroy())
</script>

<div class="chart" bind:clientWidth={width} bind:this={el}></div>

<style>
  .chart {
    width: 100%;
    min-height: 40px;
  }
  .chart :global(.u-legend) {
    color: var(--muted);
    font-size: var(--fs-xs);
  }
</style>
