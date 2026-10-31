<script lang="ts">
  import { link, router } from '../lib/router.svelte'

  const nav = [
    { href: '/', label: 'Главная', name: 'home' },
    { href: '/server', label: 'Сервер', name: 'server' },
    { href: '/audit', label: 'Аудит', name: 'audit' },
    { href: '/keys', label: 'Ключи', name: 'keys' },
    { href: '/ai', label: 'Нейросети', name: 'ai' },
  ]
</script>

<header>
  <a class="logo mono" href="/" use:link>TG PANEL</a>
  <nav>
    {#each nav as n}
      <a href={n.href} use:link class:active={router.route.name === n.name}>{n.label}</a>
    {/each}
  </nav>
</header>

<style>
  header {
    display: flex;
    align-items: center;
    gap: var(--s5);
    padding: 0 var(--s5);
    height: 48px;
    border-bottom: var(--line);
    background: var(--surface);
  }
  .logo {
    font-size: var(--fs-m);
    letter-spacing: 0.12em;
    border: 1px solid var(--accent);
    padding: 2px var(--s2);
  }
  nav {
    display: flex;
    gap: var(--s4);
  }
  nav a {
    color: var(--muted);
    padding: var(--s1) 0;
    border-bottom: 1px solid transparent;
  }
  nav a:hover,
  nav a.active {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
</style>
