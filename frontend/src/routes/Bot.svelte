<script lang="ts">
  // Bot page: header with status and buttons, tiles and charts on top (with
  // metrics), and all tabs in one row; the open tab lives in the address.
  import { api, errorText } from '../lib/api/client'
  import { store } from '../lib/api/stores.svelte'
  import { uptime } from '../lib/format'
  import { router } from '../lib/router.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'
  import StatusSquare from '../lib/ui/StatusSquare.svelte'
  import Tabs from '../lib/ui/Tabs.svelte'
  import Logs from './bot/Logs.svelte'
  import Settings from './bot/Settings.svelte'
  import Metrics from './bot/Metrics.svelte'
  import Users from './bot/Users.svelte'
  import Database from './bot/Database.svelte'

  let { id, tab }: { id: number; tab: string } = $props()
  const tabs = [
    { id: 'logs', label: 'Логи' },
    { id: 'panics', label: 'Паники' },
    { id: 'db', label: 'База данных' },
    { id: 'users', label: 'Пользователи' },
    { id: 'broadcast', label: 'Рассылка' },
    { id: 'deploy', label: 'Деплой' },
    { id: 'network', label: 'Сеть' },
    { id: 'profile', label: 'Профиль' },
    { id: 'settings', label: 'Настройки' },
  ]
  let busy = $state(false)
  let error = $state('')

  $effect(() => {
    void id
    store.refresh(id)
    const t = setInterval(() => store.refresh(id), 5000)
    return () => clearInterval(t)
  })

  const bot = $derived(store.byId(id))
  const running = $derived(bot?.state === 'running' || bot?.state === 'paused')

  async function act(op: string) {
    busy = true
    error = ''
    try {
      await api.post(`/api/bots/${id}/${op}`)
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
      store.refresh(id)
    }
  }
</script>

{#if !bot}
  <p class="muted">{store.loaded ? 'Нет такого бота' : 'Загрузка…'}</p>
{:else}
  <div class="head">
    <StatusSquare state={bot.state} size={12} />
    <h1>{bot.name}</h1>
    <span class="muted">@{bot.username}</span>
    <span class="num muted">{bot.version || 'нет версии'}</span>
    {#if bot.status?.running}<span class="num muted">аптайм {uptime(bot.status.started)}</span>{/if}
    {#if (bot.status?.restarts ?? 0) > 0}<span class="num muted">рестартов {bot.status.restarts}</span>{/if}
    <span class="grow"></span>
    {#if running}
      <HoldButton disabled={busy} onconfirm={() => act('stop')}>■ Стоп</HoldButton>
    {:else}
      <button disabled={busy} onclick={() => act('start')}>▶ Старт</button>
    {/if}
    <button disabled={busy || !running} onclick={() => act('restart')}>↻ Рестарт</button>
    <button disabled title="Пауза">⏸</button>
  </div>
  {#if error}<p class="danger err">{error}</p>{/if}

  <Metrics {bot} />

  <Tabs {tabs} active={tab} onselect={(t) => router.go(`/bots/${id}/${t}`, true)} />
  <div class="tab">
    {#if tab === 'logs'}
      <Logs botId={id} />
    {:else if tab === 'db'}
      <Database botId={id} />
    {:else if tab === 'users'}
      <Users botId={id} />
    {:else if tab === 'settings'}
      <Settings {bot} />
    {:else}
      <p class="muted">Вкладка появится в следующих шагах.</p>
    {/if}
  </div>
{/if}

<style>
  .head {
    display: flex;
    align-items: center;
    gap: var(--s3);
    margin-bottom: var(--s3);
    flex-wrap: wrap;
  }
  .grow {
    flex: 1;
  }
  .tab {
    padding-top: var(--s3);
  }
  .err {
    white-space: pre-wrap;
  }
</style>
