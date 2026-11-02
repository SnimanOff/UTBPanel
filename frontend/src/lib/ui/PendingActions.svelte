<script lang="ts">
  // Requests of an AI waiting for the owner: what will be done (the diff of
  // settings, the version), confirm by holding or reject. Shown in the
  // notification feed, the gate window and on the AI page.
  import { actions, type Action } from '../actions.svelte'
  import { store } from '../api/stores.svelte'
  import HoldButton from './HoldButton.svelte'

  let { history = false }: { history?: boolean } = $props()
  $effect(() => actions.start())

  const shown = $derived(history ? actions.items : actions.pending)
  const states: Record<string, string> = {
    pending: 'ждёт',
    running: 'выполняется',
    done: 'выполнено',
    failed: 'ошибка',
    rejected: 'отклонено',
    expired: 'истекло',
  }
  type Change = { path: string; before: unknown; after: unknown; level: string }
  function changes(a: Action): Change[] {
    const d = a.detail as { diff?: { changes?: Change[] }; config?: { diff?: { changes?: Change[] } } }
    return d.diff?.changes ?? d.config?.diff?.changes ?? []
  }
  function args(a: Action): string {
    const d = a.detail as { arguments?: unknown }
    return d.arguments ? JSON.stringify(d.arguments) : ''
  }
  const bot = (id?: number) => (id ? (store.bots.find((b) => b.id === id)?.name ?? `бот ${id}`) : '')
  const left = (ts: string) => Math.max(0, Math.round((new Date(ts).getTime() - Date.now()) / 60000))
</script>

{#if shown.length}
  <div class="actions">
    {#if actions.error}<p class="danger">{actions.error}</p>{/if}
    {#each shown as a (a.id)}
      <div class="action" class:open={a.state === 'pending'}>
        <div class="head">
          <strong>{bot(a.bot_id)}{a.bot_id ? ': ' : ''}{a.summary}</strong>
          <span class="muted">
            {a.requested_by} ·
            {#if a.state === 'pending'}ещё {left(a.expires_at)} мин{:else}{states[a.state] ?? a.state}{/if}
          </span>
        </div>
        {#each changes(a) as c}
          <div class="change">
            <span class="mono">{c.path}</span>
            <span class:danger={c.level === 'confirm'} class:muted={c.level !== 'confirm'}>
              {c.level === 'confirm' ? 'с подтверждением' : 'сразу'}
            </span>
            <del class="mono">{JSON.stringify(c.before)}</del>
            <ins class="mono">{JSON.stringify(c.after)}</ins>
          </div>
        {/each}
        {#if args(a)}<p class="mono muted args">{args(a)}</p>{/if}
        {#if a.state === 'pending'}
          <div class="buttons">
            <HoldButton disabled={actions.busy !== ''} onconfirm={() => actions.decide(a.id, true)}>Подтвердить</HoldButton>
            <button disabled={actions.busy !== ''} onclick={() => actions.decide(a.id, false)}>Отклонить</button>
          </div>
        {:else if a.state === 'failed' && a.result}
          <p class="danger mono args">{JSON.stringify(a.result)}</p>
        {/if}
      </div>
    {/each}
  </div>
{/if}

<style>
  .action {
    border: var(--line);
    padding: var(--s2);
    margin-bottom: var(--s2);
  }
  .action.open {
    border-color: var(--warn);
  }
  .head {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .head .muted {
    font-size: var(--fs-s);
  }
  .change {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0 var(--s2);
    border-bottom: var(--line);
    padding: var(--s1) 0;
    font-size: var(--fs-s);
  }
  del,
  ins {
    grid-column: 1 / -1;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    text-decoration: none;
  }
  del {
    color: var(--danger);
  }
  ins {
    color: var(--accent-hi);
  }
  .args {
    font-size: var(--fs-s);
    overflow-wrap: anywhere;
  }
  .buttons {
    display: flex;
    gap: var(--s2);
    margin-top: var(--s2);
  }
</style>
