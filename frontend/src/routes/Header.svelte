<script lang="ts">
  import { link, router } from '../lib/router.svelte'
  import { live } from '../lib/live.svelte'
  import Feed from '../lib/ui/Feed.svelte'
  import { notifications } from '../lib/notifications.svelte'
  import { actions } from '../lib/actions.svelte'
  import PendingActions from '../lib/ui/PendingActions.svelte'
  import NewBot from './NewBot.svelte'
  import { session } from '../lib/session.svelte'

  const all = [
    { href: '/', label: 'Главная', name: 'home' },
    { href: '/server', label: 'Сервер', name: 'server' },
    { href: '/audit', label: 'Аудит', name: 'audit' },
    { href: '/keys', label: 'Ключи', name: 'keys' },
    { href: '/access', label: 'Доступ', name: 'access', owner: true },
    { href: '/ai', label: 'Нейросети', name: 'ai' },
  ]
  // A person with some bots sees only the bots; "Доступ" is the owner's.
  const nav = $derived(session.limited ? all.slice(0, 1) : all.filter((n) => !n.owner || session.owner))
  let adding = $state(false)
  let feedOpen = $state(false)
  const s = $derived(live.server)
  const pct = (v?: number) => (v === undefined ? '—' : Math.round(v) + '%')
  const mbs = (v?: number) => (v === undefined ? '—' : (v / 1048576).toFixed(1))
  // The bell shows how many AI requests wait for the owner.
  $effect(() => {
    if (session.owner) return actions.start()
  })
</script>

<header>
  <a class="logo mono" href="/" use:link>TG PANEL</a>
  <div class="grow">
    {#if !session.limited}
      <a class="metrics num" href="/server" use:link title="Сервер">
        CPU {pct(s?.cpu)} · RAM {pct(s?.ram)} · диск {pct(s?.disk)} · ↓{mbs(s?.rx)} ↑{mbs(s?.tx)} МБ/с · {s?.ups ?? '—'} апд/с
      </a>
    {/if}
    <nav>
      {#each nav as n}
        <a href={n.href} use:link class:active={router.route.name === n.name}>{n.label}</a>
      {/each}
    </nav>
  </div>
  <div class="right">
    {#if !session.limited}
      <button class="bell" class:has={notifications.unread > 0} onclick={() => (feedOpen = !feedOpen)} title="Уведомления">
        🔔 <span class="num">{notifications.unread}</span>
        {#if actions.pending.length}<span class="num ask" title="Нейросеть ждёт подтверждения">· {actions.pending.length}?</span>{/if}
      </button>
      <button class="primary" onclick={() => (adding = true)}>+ Бот</button>
    {/if}
    {#if session.web}<button onclick={() => session.logout()} title={session.name}>Выйти</button>{/if}
  </div>
  {#if feedOpen}
    <div class="feed">
      <PendingActions />
      {#if notifications.unread}<button class="readall" onclick={() => notifications.readAll()}>прочитать все</button>{/if}
      <Feed
        items={notifications.items}
        onopen={(i) => {
          notifications.read(i.id)
          feedOpen = false
          if (i.href) router.go(i.href)
        }}
      />
    </div>
  {/if}
</header>

{#if adding}<NewBot onclose={() => (adding = false)} />{/if}

<style>
  .ask {
    color: var(--warn);
  }
  header {
    position: relative;
    display: flex;
    align-items: center;
    gap: var(--s5);
    padding: var(--s2) var(--s5);
    border-bottom: var(--line);
    background: var(--surface);
  }
  .logo {
    font-size: var(--fs-m);
    letter-spacing: 0.12em;
    border: 1px solid var(--accent);
    padding: 2px var(--s2);
  }
  .grow {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .metrics {
    color: var(--muted);
    font-size: var(--fs-s);
  }
  .metrics:hover {
    color: var(--text);
  }
  nav {
    display: flex;
    gap: var(--s4);
  }
  nav a {
    color: var(--muted);
    border-bottom: 1px solid transparent;
  }
  nav a:hover,
  nav a.active {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .right {
    display: flex;
    gap: var(--s2);
  }
  .bell.has {
    border-color: var(--danger);
  }
  .readall {
    width: 100%;
    border: none;
    border-bottom: var(--line);
    font-size: var(--fs-s);
  }
  .feed {
    position: absolute;
    right: var(--s5);
    top: 100%;
    width: min(380px, calc(100vw - 2 * var(--s3)));
    background: var(--surface);
    border: var(--line);
    z-index: 5;
  }
  /* Phones: the navigation goes to its own scrolling line under the logo
     and the buttons. */
  @media (max-width: 700px) {
    header {
      flex-wrap: wrap;
      gap: var(--s2) var(--s3);
      padding: var(--s2) var(--s3);
    }
    .grow {
      order: 3;
      flex-basis: 100%;
      min-width: 0;
    }
    .right {
      margin-left: auto;
    }
    nav {
      overflow-x: auto;
      white-space: nowrap;
      padding-bottom: 2px;
    }
    .metrics {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .feed {
      right: var(--s3);
    }
  }
</style>
