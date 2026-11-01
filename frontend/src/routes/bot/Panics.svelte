<script lang="ts">
  // Panics: groups by kind and the first frame of the bot's code, with
  // counts, versions and the last stack trace.
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import { utc } from '../../lib/format'

  let { botId }: { botId: number } = $props()
  type G = Schemas['PanicGroup']
  let groups = $state<G[]>([])
  let open = $state<number | null>(null)
  type CU = { id: number; ts: string; status: number; reason: string; body: unknown }
  let crashes = $state<CU[]>([])
  let openCU = $state<number | null>(null)
  let error = $state('')

  async function load() {
    try {
      groups = await api.get<G[]>(`/api/bots/${botId}/panics`)
      crashes = await api.get<CU[]>(`/api/bots/${botId}/crash-updates`)
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

<h2 class="sub">Апдейты в момент падения</h2>
{#if crashes.length === 0}
  <p class="muted">Нет. Включите «Сохранять апдейты при падениях» в настройках бота — тела апдейтов, бывших в обработке при падении или ответе 5xx, сохранятся здесь (последние 20, 7 дней).</p>
{/if}
{#each crashes as cu (cu.id)}
  {@const near = groups.find((g) => Math.abs(new Date(g.last_at).getTime() - new Date(cu.ts).getTime()) < 10000)}
  <section class="group crash">
    <button class="head" onclick={() => (openCU = openCU === cu.id ? null : cu.id)}>
      <span class="num">{utc(cu.ts, true)}</span>
      <span>{cu.reason === 'crash' ? 'падение' : cu.reason === 'timeout' ? 'таймаут' : `ответ ${cu.status}`}</span>
      {#if near}<span class="muted">· {near.kind}</span>{/if}
    </button>
    {#if openCU === cu.id}<pre>{JSON.stringify(cu.body, null, 2)}</pre>{/if}
  </section>
{/each}

<style>
  .sub {
    font-size: var(--fs-m);
    margin: var(--s4) 0 var(--s2);
  }
  .group.crash {
    border-left-color: var(--warn);
  }
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
