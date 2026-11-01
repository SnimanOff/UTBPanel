<script lang="ts">
  // Bot card: status, name (click to rename in place), @username, uptime,
  // version, panics badge, live mini chart, users; ▶/■ is one button by
  // status (stop by holding), ↻ restart, ⏸ pause. Red border on a problem.
  import { api, errorText, type BotView } from '../lib/api/client'
  import { store } from '../lib/api/stores.svelte'
  import { compact, uptime } from '../lib/format'
  import { live } from '../lib/live.svelte'
  import { router } from '../lib/router.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'
  import Spark from '../lib/ui/Spark.svelte'
  import StatusSquare from '../lib/ui/StatusSquare.svelte'

  let { bot }: { bot: BotView & { panics_24h?: number; users?: number } } = $props()
  let editing = $state(false)
  let name = $state('')
  let busy = $state(false)
  let error = $state('')

  const running = $derived(bot.state === 'running' || bot.state === 'paused')
  const problem = $derived(bot.state === 'crashed' || bot.state === 'error' || !!error)
  const l = $derived(live.bots[bot.id])

  async function act(op: string) {
    busy = true
    error = ''
    try {
      await api.post(`/api/bots/${bot.id}/${op}`)
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
      store.refresh(bot.id)
    }
  }

  function startRename(e: Event) {
    e.stopPropagation()
    name = bot.name
    editing = true
  }

  async function rename() {
    editing = false
    if (!name.trim() || name === bot.name) return
    try {
      await api.patch(`/api/bots/${bot.id}`, { name: name.trim() })
      store.refresh(bot.id)
    } catch (e) {
      error = errorText(e)
    }
  }

  function open(e: MouseEvent | KeyboardEvent) {
    if ((e.target as HTMLElement).closest('button, input')) return
    router.go(`/bots/${bot.id}`)
  }
</script>

<div class="card" class:problem role="link" tabindex="0" onclick={open} onkeydown={(e) => e.key === 'Enter' && open(e)}>
  <div class="row">
    <StatusSquare state={bot.state} />
    {#if editing}
      <!-- svelte-ignore a11y_autofocus -->
      <input class="rename" bind:value={name} autofocus onblur={rename} onkeydown={(e) => e.key === 'Enter' && rename()} />
    {:else}
      <button class="name" title="Переименовать" onclick={startRename}>{bot.name}</button>
    {/if}
    <span class="muted user">@{bot.username}</span>
  </div>
  <div class="row muted small">
    <span class="num">{bot.status?.running ? uptime(bot.status.started) : '—'}</span>
    <span class="num">{bot.version || 'нет версии'}</span>
    {#if bot.maintenance}<span class="maint">{bot.maintenance}</span>{/if}
    {#if (bot.panics_24h ?? 0) > 0}<span class="panics" title="Паники за 24 ч">⚠ {bot.panics_24h}</span>{/if}
  </div>
  <div class="row small">
    <Spark values={l?.history ?? []} />
    <span class="num muted">{l ? l.ups.toFixed(1) : '0'} апд/с</span>
    {#if bot.users !== undefined}<span class="num muted">{compact(bot.users)} польз</span>{/if}
  </div>
  <div class="row buttons">
    {#if running}
      <HoldButton disabled={busy} onconfirm={() => act('stop')} title="Стоп — удерживайте">■</HoldButton>
    {:else}
      <button disabled={busy} onclick={() => act('start')} title="Старт">▶</button>
    {/if}
    <button disabled={busy || !running} onclick={() => act('restart')} title="Рестарт">↻</button>
    <button disabled={busy || !running} class:active={bot.state === 'paused'} onclick={() => act(bot.state === 'paused' ? 'resume' : 'pause')}
      title={bot.state === 'paused' ? 'Снять паузу' : 'Пауза: пользователи получат текст техработ'}>⏸</button>
  </div>
  {#if error}<div class="danger small err">{error}</div>{/if}
</div>

<style>
  .card {
    background: var(--surface);
    border: var(--line);
    padding: var(--s3);
    display: flex;
    flex-direction: column;
    gap: var(--s2);
    cursor: pointer;
  }
  .card:hover {
    border-color: var(--accent);
  }
  .card.problem {
    border-color: var(--danger);
  }
  .row {
    display: flex;
    align-items: center;
    gap: var(--s2);
  }
  .small {
    font-size: var(--fs-s);
  }
  .name {
    border: none;
    background: none;
    padding: 0;
    min-height: 0;
    font-weight: 500;
    cursor: text;
  }
  .rename {
    min-height: 22px;
    padding: 0 var(--s1);
    width: 160px;
  }
  .user {
    margin-left: auto;
  }
  .panics {
    color: var(--danger);
    margin-left: auto;
  }
  .maint {
    color: var(--warn);
  }
  .buttons button,
  .buttons :global(.hold) {
    min-width: 34px;
  }
  .active {
    border-color: var(--accent-hi);
  }
  .err {
    white-space: pre-wrap;
  }
</style>
