<script lang="ts">
  // Panics: groups by kind and the first frame of the bot's code, with
  // counts, versions and the last stack trace.
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import { utc } from '../../lib/format'

  let { botId }: { botId: number } = $props()
  type G = Schemas['PanicGroup']
  let groups = $state<G[]>([])
  let open = $state<number | null>(null)
  let error = $state('')

  async function load() {
    try {
      groups = await api.get<G[]>(`/api/bots/${botId}/panics`)
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void botId
    load()
    const t = setInterval(load, 15000)
    return () => clearInterval(t)
  })
</script>

{#if error}<p class="danger">{error}</p>{/if}
{#if groups.length === 0}
  <p class="muted">Паник не было. Блоки panic: и fatal error: в выводе бота появятся здесь, сгруппированные по месту в коде.</p>
{/if}
{#each groups as g (g.id)}
  <section class="group" class:fixed={g.maybe_fixed}>
    <button class="head" onclick={() => (open = open === g.id ? null : g.id)}>
      <span class="kind">{g.kind}</span>
      <span class="num count">{g.count}×</span>
      {#if g.count_24h}<span class="num danger">{g.count_24h} за сутки</span>{/if}
      {#if g.maybe_fixed}<span class="ok">возможно исправлено</span>{/if}
    </button>
    <div class="meta">
      <span class="num">{g.frame || 'место не найдено'}</span>
      <span class="muted">· {utc(g.first_at)} — {utc(g.last_at)} · версии {g.versions.join(', ') || '—'}</span>
    </div>
    {#if open === g.id}<pre>{g.last_stack}</pre>{/if}
  </section>
{/each}

<style>
  .group {
    border: var(--line);
    border-left: 3px solid var(--danger);
    padding: var(--s2) var(--s3);
    margin-bottom: var(--s2);
  }
  .group.fixed {
    border-left-color: var(--muted);
    opacity: 0.8;
  }
  .head {
    display: flex;
    gap: var(--s3);
    align-items: baseline;
    width: 100%;
    background: none;
    border: none;
    padding: 0;
    text-align: left;
    cursor: pointer;
  }
  .kind {
    font-weight: 600;
    flex: 1;
  }
  .meta {
    font-size: var(--fs-s);
    margin-top: var(--s1);
  }
  pre {
    margin-top: var(--s2);
    max-height: 420px;
    overflow: auto;
    font-size: var(--fs-s);
    background: var(--surface-2);
    padding: var(--s2);
  }
  .ok {
    color: var(--accent-hi);
  }
</style>
