<script lang="ts">
  // The small window of gate.exe (and /mini in a browser): bots with their
  // status and buttons — stop by holding — and the latest notifications.
  // Requests of the AI wait here for the owner as well.
  import { api, errorText } from '../lib/api/client'
  import { pollBots, store } from '../lib/api/stores.svelte'
  import { notifications } from '../lib/notifications.svelte'
  import { live } from '../lib/live.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'
  import PendingActions from '../lib/ui/PendingActions.svelte'
  import StatusSquare from '../lib/ui/StatusSquare.svelte'

  let busy = $state<number | null>(null)
  let error = $state('')
  $effect(() => pollBots(5000))

  async function act(id: number, op: string) {
    busy = id
    error = ''
    try {
      await api.post(`/api/bots/${id}/${op}`)
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = null
      store.refresh(id)
    }
  }
  // The full panel opens in the browser; the small window stays small.
  const full = (href = '/') => window.open(href, '_blank')
</script>

<div class="mini">
  <header>
    <span class="mono logo">TG PANEL</span>
    <span class="grow"></span>
    <button onclick={() => full()}>Открыть панель</button>
  </header>
  {#if error}<p class="danger">{error}</p>{/if}
  <PendingActions />
  <ul class="bots">
    {#each store.bots as b (b.id)}
      {@const running = b.state === 'running' || b.state === 'paused'}
      <li class:problem={notifications.hasProblem(b.id) || b.state === 'crashed' || b.state === 'error'}>
        <StatusSquare state={b.state} size={10} />
        <button class="name" onclick={() => full(`/bots/${b.id}`)}>{b.name}</button>
        <span class="num muted">{live.bots[b.id]?.ups ?? 0} апд/с</span>
        {#if b.maintenance}<span class="maint">{b.maintenance}</span>{/if}
        <span class="grow"></span>
        {#if running}
          <HoldButton disabled={busy === b.id} onconfirm={() => act(b.id, 'stop')}>■</HoldButton>
          <button disabled={busy === b.id} title="Рестарт" onclick={() => act(b.id, 'restart')}>↻</button>
        {:else}
          <button disabled={busy === b.id} title="Старт" onclick={() => act(b.id, 'start')}>▶</button>
        {/if}
      </li>
    {:else}
      <li class="muted">{store.loaded ? 'Ботов нет' : 'Загрузка…'}</li>
    {/each}
  </ul>
  <h2>Уведомления</h2>
  <ul class="notes">
    {#each notifications.items.slice(0, 8) as n (n.id)}
      <li class={n.severity} class:unread={!n.read}>
        <button onclick={() => (notifications.read(n.id), n.href && full(n.href))}>
          <span class="num muted">{n.ts.slice(11, 16)}</span> {n.text}
        </button>
      </li>
    {:else}
      <li class="muted">Событий нет</li>
    {/each}
  </ul>
</div>

<style>
  .mini {
    padding: var(--s3);
    max-width: 480px;
  }
  header {
    display: flex;
    align-items: center;
    margin-bottom: var(--s3);
  }
  .logo {
    border: var(--line);
    padding: 2px var(--s2);
  }
  .grow {
    flex: 1;
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .bots li {
    display: flex;
    align-items: center;
    gap: var(--s2);
    padding: var(--s1) var(--s2);
    border: var(--line);
    margin-bottom: var(--s1);
  }
  .bots li.problem {
    border-color: var(--danger);
  }
  .name {
    border: none;
    background: none;
    padding: 0;
    font-weight: 600;
  }
  .maint {
    color: var(--warn);
    font-size: var(--fs-s);
  }
  h2 {
    font-size: var(--fs-s);
    color: var(--muted);
    margin: var(--s3) 0 var(--s1);
  }
  .notes button {
    border: none;
    background: none;
    text-align: left;
    padding: 2px 0;
    font-size: var(--fs-s);
  }
  .notes li.error {
    border-left: 2px solid var(--danger);
    padding-left: var(--s1);
  }
  .notes li.unread button {
    color: var(--text);
    font-weight: 600;
  }
</style>
