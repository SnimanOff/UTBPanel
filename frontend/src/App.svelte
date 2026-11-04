<script lang="ts">
  import { router } from './lib/router.svelte'
  import { live } from './lib/live.svelte'
  import { notifications } from './lib/notifications.svelte'
  import { session } from './lib/session.svelte'
  import Header from './routes/Header.svelte'
  import Placeholder from './routes/Placeholder.svelte'
  import Home from './routes/Home.svelte'
  import Bot from './routes/Bot.svelte'
  import Keys from './routes/Keys.svelte'
  import Audit from './routes/Audit.svelte'
  import Server from './routes/Server.svelte'
  import Mini from './routes/Mini.svelte'
  import AI from './routes/AI.svelte'
  import Access from './routes/Access.svelte'
  import Login from './routes/Login.svelte'
  import Invite from './routes/Invite.svelte'

  const titles: Record<string, string> = {
    home: 'Главная',
    bot: 'Бот',
    server: 'Сервер',
    audit: 'Аудит',
    keys: 'Ключи',
    ai: 'Нейросети',
    access: 'Доступ',
    notfound: 'Нет такой страницы',
  }
  const r = $derived(router.route)
  // The invitation page works without a session.
  if (router.route.name !== 'invite') session.load()
  const ready = $derived(session.loaded && !session.login)
  $effect(() => {
    if (ready) return live.use()
  })
  $effect(() => {
    // People with some bots do not get the feed of the whole panel.
    if (ready && !session.limited) notifications.start()
  })
</script>

{#if r.name === 'invite'}
  <Invite token={r.token} />
{:else if !session.loaded}
  <!-- checking the session -->
{:else if session.login}
  <Login />
{:else if r.name === 'mini'}
  <Mini />
{:else}
  <Header />
  <main>
    {#if r.name === 'home'}
      <Home />
    {:else if r.name === 'bot'}
      {#key r.id}<Bot id={r.id} tab={r.tab} />{/key}
    {:else if session.limited}
      <Placeholder title="Эта страница недоступна" />
    {:else if r.name === 'keys'}
      <Keys />
    {:else if r.name === 'audit'}
      <Audit />
    {:else if r.name === 'server'}
      <Server />
    {:else if r.name === 'ai'}
      <AI />
    {:else if r.name === 'access'}
      <Access />
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
  @media (max-width: 700px) {
    main {
      padding: var(--s3);
    }
  }
</style>
