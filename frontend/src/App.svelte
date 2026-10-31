<script lang="ts">
  import { router } from './lib/router.svelte'
  import Header from './routes/Header.svelte'
  import Placeholder from './routes/Placeholder.svelte'

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
</script>

{#if r.name === 'mini'}
  <Placeholder title="Мини-окно" />
{:else}
  <Header />
  <main>
    <Placeholder title={titles[r.name]} />
  </main>
{/if}

<style>
  main {
    padding: var(--s4) var(--s5);
    max-width: 1600px;
    margin: 0 auto;
  }
</style>
