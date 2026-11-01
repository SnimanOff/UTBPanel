<script lang="ts">
  import { router } from './lib/router.svelte'
  import { live } from './lib/live.svelte'
  import { notifications } from './lib/notifications.svelte'
  import Header from './routes/Header.svelte'
  import Placeholder from './routes/Placeholder.svelte'
  import Home from './routes/Home.svelte'
  import Bot from './routes/Bot.svelte'
  import Keys from './routes/Keys.svelte'
  import Audit from './routes/Audit.svelte'
  import Server from './routes/Server.svelte'

  const titles: Record<string, string> = {
    home: 'Главная',
    bot: 'Бот',
    server: 'Сервер',
    audit: 'Аудит',
    keys: 'Ключи',
    ai: 'Нейросети',
    notfound: 'Нет такой страницы',
  }
  const r = $derived(router.route)
  $effect(() => live.use())
  notifications.start()
</script>

{#if r.name === 'mini'}
  <Placeholder title="Мини-окно" />
{:else}
  <Header />
  <main>
    {#if r.name === 'home'}
      <Home />
    {:else if r.name === 'bot'}
      {#key r.id}<Bot id={r.id} tab={r.tab} />{/key}
    {:else if r.name === 'keys'}
      <Keys />
    {:else if r.name === 'audit'}
      <Audit />
    {:else if r.name === 'server'}
      <Server />
    {:else}
      <Placeholder title={titles[r.name]} />
    {/if}
  </main>
{/if}

<style>
  main {
    padding: var(--s4) var(--s5);
    max-width: 1600px;
    margin: 0 auto;
  }
</style>
