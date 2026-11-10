<script lang="ts">
  // Bot page: header with status and buttons, tiles and charts on top (with
  // metrics), and all tabs in one row; the open tab lives in the address.
  import { api, errorText } from '../lib/api/client'
  import { store } from '../lib/api/stores.svelte'
  import { uptime } from '../lib/format'
  import { link, router } from '../lib/router.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'
  import StatusSquare from '../lib/ui/StatusSquare.svelte'
  import Tabs from '../lib/ui/Tabs.svelte'
  import Logs from './bot/Logs.svelte'
  import Settings from './bot/Settings.svelte'
  import Metrics from './bot/Metrics.svelte'
  import Users from './bot/Users.svelte'
  import Database from './bot/Database.svelte'
  import Network from './bot/Network.svelte'
  import Maintenance from './bot/Maintenance.svelte'
  import Profile from './bot/Profile.svelte'
  import Broadcast from './bot/Broadcast.svelte'
  import Deploy from './bot/Deploy.svelte'
  import Panics from './bot/Panics.svelte'

  let { id, tab }: { id: number; tab: string } = $props()
  const panics = $derived(store.panics[String(id)] ?? 0)
  const tabs = $derived([
    { id: 'logs', label: 'Логи' },
    { id: 'panics', label: panics ? `Паники · ${panics}` : 'Паники' },
    { id: 'db', label: 'База данных' },
    { id: 'users', label: 'Пользователи' },
    { id: 'broadcast', label: 'Рассылка' },
    { id: 'deploy', label: 'Деплой' },
    { id: 'network', label: 'Сеть' },
    { id: 'profile', label: 'Профиль' },
    { id: 'settings', label: 'Настройки' },
  ])
  let busy = $state(false)
  let error = $state('')

  $effect(() => {
    void id
    store.refresh(id)
    store.loadPanics()
    const t = setInterval(() => (store.refresh(id), store.loadPanics()), 5000)
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
	{#if bot.sdk}<span class="muted">SDK v{bot.sdk.version}: {bot.sdk.modules.join(', ')}{bot.sdk.connected ? '' : ' · нет связи'}</span>{/if}
    {#if bot.status?.running}<span class="num muted">аптайм {uptime(bot.status.started)}</span>{/if}
    {#if bot.maintenance}<span class="maint">{bot.maintenance}</span>{/if}
    {#if bot.canary}<span class="canary">{bot.canary}</span>{/if}
    {#if bot.test_for}
      <a class="canary" href="/bots/{bot.test_for}/deploy" use:link>тестовый бот для {store.byId(bot.test_for)?.name ?? `#${bot.test_for}`}</a>
    {/if}
    {#if (bot.status?.restarts ?? 0) > 0}<span class="num muted">рестартов {bot.status.restarts}</span>{/if}
    <span class="grow"></span>
    {#if running}
      <HoldButton disabled={busy} onconfirm={() => act('stop')}>■ Стоп</HoldButton>
    {:else}
      <button disabled={busy} onclick={() => act('start')}>▶ Старт</button>
    {/if}
    <button disabled={busy || !running} onclick={() => act('restart')}>↻ Рестарт</button>
    <button disabled={busy || !running} onclick={() => act(bot.state === 'paused' ? 'resume' : 'pause')}>
      {bot.state === 'paused' ? '▶ Снять паузу' : '⏸ Пауза'}
    </button>
  </div>
  {#if error}<p class="danger err">{error}</p>{/if}

  <Metrics {bot} />

  <Tabs {tabs} active={tab} onselect={(t) => router.go(`/bots/${id}/${t}`, true)} />
  <div class="tab">
    {#if tab === 'logs'}
      <Logs botId={id} />
    {:else if tab === 'panics'}
      <Panics botId={id} />
    {:else if tab === 'db'}
      <Database botId={id} />
    {:else if tab === 'network'}
      <Network botId={id} />
    {:else if tab === 'users'}
      <Users botId={id} />
    {:else if tab === 'deploy'}
      <Deploy botId={id} />
      <h2 class="sub">Техработы</h2>
      <Maintenance botId={id} />
    {:else if tab === 'broadcast'}
      <Broadcast botId={id} />
    {:else if tab === 'profile'}
      <Profile botId={id} />
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
  .canary {
    color: var(--accent-text);
  }
  .maint {
    color: var(--warn);
  }
  .grow {
    flex: 1;
  }
  .tab {
    padding-top: var(--s3);
  }
  .sub {
    font-size: var(--fs-m);
    margin-bottom: var(--s2);
  }
  .err {
    white-space: pre-wrap;
  }
</style>
