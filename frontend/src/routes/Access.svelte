<script lang="ts">
  // Access: passkeys for phones. The owner invites a person (all bots or
  // some, a role), sends the link, compares the four words and approves by
  // holding the button. Revoking works at once.
  import { api, errorText, type Schemas } from '../lib/api/client'
  import { store } from '../lib/api/stores.svelte'
  import { utc } from '../lib/format'
  import Box from '../lib/ui/Box.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'

  type Access = Schemas['WebAccess']
  type List = { enabled: boolean; url: string; accesses: Access[] }
  let data = $state<List>()
  let error = $state('')
  let link = $state<{ url: string; expires_at: string; name: string } | null>(null)

  let name = $state('')
  let role = $state<'view' | 'manage' | 'full'>('view')
  let allBots = $state(false)
  let picked = $state<Record<number, boolean>>({})
  let busy = $state(false)

  async function load() {
    try {
      data = await api.get<List>('/api/access')
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    load()
    store.load()
    // A phone may be waiting for approval.
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  })

  const roles = { view: 'Просмотр', manage: 'Управление', full: 'Все права (как дочерний ключ)' }
  const botName = (id: number) => store.byId(id)?.name ?? `#${id}`

  async function invite() {
    busy = true
    error = ''
    try {
      const bots = Object.entries(picked).filter(([, v]) => v).map(([k]) => Number(k))
      const r = await api.post<{ url: string; expires_at: string; access: Access }>('/api/access', {
        name: name.trim(), role, all_bots: allBots || role === 'full', bots,
      })
      link = { url: r.url, expires_at: r.expires_at, name: r.access.name }
      name = ''
      picked = {}
      await load()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
  async function reinvite(a: Access) {
    try {
      const r = await api.post<{ url: string; expires_at: string }>(`/api/access/${a.id}/invite`)
      link = { ...r, name: a.name }
    } catch (e) {
      error = errorText(e)
    }
  }
  async function act(fn: () => Promise<unknown>) {
    error = ''
    try {
      await fn()
      await load()
    } catch (e) {
      error = errorText(e)
    }
  }
  const copy = (s: string) => navigator.clipboard?.writeText(s)
</script>

<h1>Доступ по passkey</h1>
<p class="muted">
  Вход с телефона без gate: Face ID или отпечаток. Можно дать человеку только нужных ботов.
  {#if data?.enabled}Адрес: <span class="mono">{data.url}</span>{/if}
</p>
{#if data && !data.enabled}
  <p class="danger">Нужен домен: задайте DOMAIN в .env и откройте порт 9443. Без домена и сертификата Let's Encrypt телефоны не создают passkey.</p>
{/if}
{#if error}<p class="danger">{error}</p>{/if}

<div class="stack">
{#if link}
  <Box title={`Ссылка для «${link.name}»`}>
    <p class="mono url">{link.url}</p>
    <div class="row">
      <button class="primary" onclick={() => copy(link!.url)}>Копировать</button>
      <span class="muted">одноразовая, до {utc(link.expires_at)}. Откройте её на телефоне.</span>
      <button onclick={() => (link = null)}>Скрыть</button>
    </div>
  </Box>
{/if}

{#if data?.enabled}
  <Box title="Пригласить">
    <div class="row">
      <input bind:value={name} placeholder="имя, например «Мой iPhone» или «Анна, маркетинг»" class="grow" maxlength="60" />
      <select bind:value={role}>
        {#each Object.entries(roles) as [k, v]}<option value={k}>{v}</option>{/each}
      </select>
    </div>
    {#if role !== 'full'}
      <label><input type="checkbox" bind:checked={allBots} /> все боты</label>
      {#if !allBots}
        <div class="bots">
          {#each store.bots as b (b.id)}
            <label><input type="checkbox" bind:checked={picked[b.id]} /> {b.name}</label>
          {/each}
        </div>
      {/if}
    {/if}
    <p class="muted small">
      Просмотр — логи, метрики, пользователи. Управление — ещё рестарт, пауза, техработы, настройки и деплой этих ботов.
      Ключи, бэкапы, рассылки, базы и другие боты недоступны обоим.
    </p>
    <button class="primary" onclick={invite}
      disabled={busy || !name.trim() || (role !== 'full' && !allBots && !Object.values(picked).some(Boolean))}>Создать ссылку</button>
  </Box>
{/if}

{#each data?.accesses ?? [] as a (a.id)}
  <Box title={a.name}>
    {#snippet actions()}
      <button onclick={() => reinvite(a)}>ещё устройство</button>
      <HoldButton onconfirm={() => act(() => api.del(`/api/access/${a.id}`))}>отозвать</HoldButton>
    {/snippet}
    <p class="muted">
      {roles[a.role]} · {a.all_bots ? 'все боты' : a.bots.map(botName).join(', ')}
      {#if a.invite}· ссылка ждёт до {utc(a.invite.expires_at)}{/if}
    </p>
    {#each a.credentials as c (c.id)}
      <div class="cred" class:waiting={!c.approved}>
        <span class="mono code">{c.code}</span>
        <span class="muted small device" title={c.device}>{c.device}</span>
        <span class="muted small">{c.ip}</span>
        {#if c.approved}
          <span class="muted small">{c.last_used ? 'вход ' + utc(c.last_used) : 'ещё не входил'}</span>
          <HoldButton onconfirm={() => act(() => api.del(`/api/access/credentials/${c.id}`))}>удалить</HoldButton>
        {:else}
          <span>сверьте слова с телефоном</span>
          <HoldButton danger={false} onconfirm={() => act(() => api.post(`/api/access/credentials/${c.id}/approve`))}>Подтвердить</HoldButton>
          <button onclick={() => act(() => api.del(`/api/access/credentials/${c.id}`))}>отклонить</button>
        {/if}
      </div>
    {:else}
      <p class="muted">Ключей входа пока нет.</p>
    {/each}
  </Box>
{/each}
</div>

<style>
  h1 { margin-bottom: var(--s2); }
  /* Scoped to this page: a bare global rule pushed down every box after the
     first in the grids of other pages. */
  .stack > :global(.box) + :global(.box) { margin-top: var(--s3); }
  .row { display: flex; gap: var(--s3); align-items: center; flex-wrap: wrap; margin-bottom: var(--s2); }
  .grow { flex: 1; min-width: 220px; }
  .bots { display: flex; flex-wrap: wrap; gap: var(--s2) var(--s4); margin: var(--s2) 0; }
  .url { word-break: break-all; }
  .cred { display: flex; gap: var(--s3); align-items: center; flex-wrap: wrap; padding: var(--s2) 0; border-bottom: var(--line); }
  .cred.waiting .code { color: var(--warn); }
  .code { font-size: var(--fs-m); }
  .device { max-width: 280px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .small { font-size: var(--fs-s); }
</style>
