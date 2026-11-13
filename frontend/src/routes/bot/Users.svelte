<script lang="ts">
  // Users: search by id or username, filter, pages; audience breakdown.
  // A ban needs a reason; "во всех ботах" is confirmed by holding.
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import { utc } from '../../lib/format'
  import Box from '../../lib/ui/Box.svelte'
  import HoldButton from '../../lib/ui/HoldButton.svelte'
  import Payments from './Payments.svelte'

  let { botId }: { botId: number } = $props()
  let payUser = $state(0)
  type List = Schemas['UserList']
  type Audience = Schemas['Audience']
  const PAGE = 100
  let q = $state('')
  let filter = $state('all')
  let page = $state(0)
  let list = $state<List>({ users: [], total: 0 })
  let aud = $state<Audience>()
  let audienceError = $state('')
  let error = $state('')

  async function load() {
    error = ''
    try {
      const p = new URLSearchParams({ filter, limit: String(PAGE), offset: String(page * PAGE) })
      if (q.trim()) p.set('q', q.trim())
      list = await api.get<List>(`/api/bots/${botId}/users?${p}`)
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void [botId, filter, page]
    load()
  })
  $effect(() => {
    let current = true
    aud = undefined
    audienceError = ''
    api.get<Audience>(`/api/bots/${botId}/audience`).then(
      (a) => { if (current) aud = a },
      (e) => { if (current) audienceError = errorText(e) },
    )
    return () => { current = false }
  })

  let banFor = $state<number | null>(null)
  let reason = $state('')
  let hours = $state(0)
  let everywhere = $state(false)

  function openBan(id: number) {
    banFor = id
    reason = ''
    hours = 0
    everywhere = false
  }
  async function ban() {
    if (banFor === null || !reason.trim()) return
    error = ''
    try {
      const url = everywhere ? `/api/users/${banFor}/ban` : `/api/bots/${botId}/users/${banFor}/ban`
      await api.post(url, { reason: reason.trim(), hours })
      banFor = null
      load()
    } catch (e) {
      error = errorText(e)
    }
  }
  async function unban(id: number) {
    try {
      await api.post(`/api/bots/${botId}/users/${id}/unban`)
      load()
    } catch (e) {
      error = errorText(e)
    }
  }

  const pages = $derived(Math.max(1, Math.ceil(list.total / PAGE)))
  const number = new Intl.NumberFormat('ru-RU')
  const percent = new Intl.NumberFormat('ru-RU', { style: 'percent', maximumFractionDigits: 1 })
  const pct = (n: number, of: number) => percent.format(of ? n / of : 0)
  const activity = $derived(aud?.activity)
  const segments = $derived(activity ? [
    { label: 'Регулярные · 3+ дня', count: activity.regular, color: 'var(--accent-hi)' },
    { label: 'Активные · 1–2 дня', count: activity.recent, color: '#679ac6' },
    { label: 'Разовые, не вернулись', count: activity.one_day, color: 'var(--warn)' },
    { label: 'Ранее активные', count: activity.inactive, color: 'var(--muted)' },
    { label: 'Заблокировали бота', count: activity.blocked, color: 'var(--danger-text)' },
    ...(activity.untracked > 0
      ? [{ label: 'Без данных об активности', count: activity.untracked, color: 'var(--border)' }]
      : []),
  ] : [])
</script>

<div class="layout">
  <aside>
    <Box title="Аудитория">
      {#if aud}
        <div class="row"><span class="muted">Всего</span><span class="num">{number.format(aud.total)}</span></div>
        {#if aud.total > 0}
          {#if activity}
            <h3>Активность за 30 дней · UTC</h3>
            <div class="distribution" aria-hidden="true">
              {#each segments as segment (segment.label)}
                <span style:background={segment.color} style:flex-grow={segment.count}></span>
              {/each}
            </div>
            <ul class="segments">
              {#each segments as segment (segment.label)}
                <li>
                  <span class="swatch" style:background={segment.color} aria-hidden="true"></span>
                  <span>{segment.label}</span>
                  <span class="num">{number.format(segment.count)}</span>
                  <span class="num muted">{pct(segment.count, aud.total)}</span>
                </li>
              {/each}
            </ul>
            <p class="muted note">Разовые и ранее активные не заходили 30 дней. У разовых был только один день активности.</p>
          {/if}
          <div class="row"><span class="muted">Premium</span><span class="num">{pct(aud.premium, aud.total)}</span></div>
          <details>
            <summary>Языки и источники</summary>
            <h3>Языки</h3>
            {#each aud.languages as l}<div class="row"><span>{l.key}</span><span class="num">{pct(l.count, aud.total)}</span></div>{/each}
            <h3>Источники</h3>
            {#each aud.sources as s}<div class="row"><span>{s.key}</span><span class="num">{s.count}</span></div>{:else}<p class="muted">—</p>{/each}
          </details>
        {/if}
      {:else if audienceError}
        <p class="danger">Не удалось загрузить аудиторию: {audienceError}</p>
      {:else}
        <span class="muted">Загрузка…</span>
      {/if}
    </Box>
  </aside>
  <div class="users">
    <div class="bar">
      <form onsubmit={(e) => (e.preventDefault(), (page = 0), load())}>
        <input bind:value={q} placeholder="id или @username" />
        <button>Найти</button>
      </form>
      <div class="filters">
        {#each [['all', 'все'], ['banned', 'забаненные'], ['blocked', 'заблокировавшие']] as [id, label]}
          <button class:active={filter === id} onclick={() => ((filter = id), (page = 0))}>{label}</button>
        {/each}
      </div>
      <span class="muted num">{list.total}</span>
    </div>
    {#if error}<p class="danger">{error}</p>{/if}
    <div class="table-scroll">
    <table>
      <thead>
        <tr><th>ID</th><th>Username</th><th>Источник</th><th>Язык</th><th>Premium</th><th>Первый визит</th><th>Последний</th><th>Апдейтов</th><th></th></tr>
      </thead>
      <tbody>
        {#each list.users as u (u.id)}
          <tr>
            <td class="num">{u.id}</td>
            <td>{u.username ? '@' + u.username : '—'}</td>
            <td class="muted">{u.source || '—'}</td>
            <td>{u.language || '—'}</td>
            <td>{u.premium ? '★' : ''}</td>
            <td class="num muted">{utc(u.first_seen)}</td>
            <td class="num">{utc(u.last_seen)}</td>
            <td class="num">{u.updates}</td>
            <td class="acts">
              {#if u.banned}
                <span class="danger" title={u.ban_reason}>бан</span>
                <button onclick={() => unban(u.id)}>разбанить</button>
              {:else}
                <button onclick={() => openBan(u.id)}>бан</button>
              {/if}
              {#if u.blocked}<span class="muted">заблокировал</span>{/if}
              <button onclick={() => (payUser = u.id)} title="Платежи пользователя">платежи</button>
            </td>
          </tr>
          {#if banFor === u.id}
            <tr class="banrow">
              <td colspan="9">
                <form onsubmit={(e) => (e.preventDefault(), !everywhere && ban())}>
                  <input bind:value={reason} placeholder="причина (обязательно)" required />
                  <select bind:value={hours}>
                    <option value={0}>навсегда</option>
                    <option value={1}>1 час</option>
                    <option value={24}>сутки</option>
                    <option value={168}>неделя</option>
                    <option value={720}>30 дней</option>
                  </select>
                  <label><input type="checkbox" bind:checked={everywhere} /> во всех ботах</label>
                  {#if everywhere}
                    <HoldButton disabled={!reason.trim()} onconfirm={ban}>Забанить везде</HoldButton>
                  {:else}
                    <button class="primary" disabled={!reason.trim()}>Забанить</button>
                  {/if}
                  <button type="button" onclick={() => (banFor = null)}>Отмена</button>
                </form>
              </td>
            </tr>
          {/if}
        {:else}
          <tr><td colspan="9" class="muted">Пользователей нет</td></tr>
        {/each}
      </tbody>
    </table>
    </div>
    {#if pages > 1}
      <div class="pager">
        <button disabled={page === 0} onclick={() => page--}>←</button>
        <span class="num muted">{page + 1} / {pages}</span>
        <button disabled={page >= pages - 1} onclick={() => page++}>→</button>
      </div>
    {/if}
    <div class="payments"><Payments {botId} bind:user={payUser} /></div>
  </div>
</div>

<style>
  .layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 300px;
    grid-template-areas: 'users audience';
    gap: var(--s3);
    align-items: start;
  }
  aside {
    grid-area: audience;
    min-width: 0;
  }
  .users {
    grid-area: users;
  }
  .table-scroll {
    overflow-x: auto;
  }
  .table-scroll table {
    display: table;
  }
  .bar {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s3);
    align-items: center;
    margin-bottom: var(--s2);
  }
  form {
    display: flex;
    gap: var(--s2);
  }
  .filters {
    display: flex;
  }
  .filters button + button {
    border-left: none;
  }
  .filters .active {
    border-color: var(--accent);
    background: var(--surface-2);
  }
  th,
  td {
    text-align: left;
    padding: var(--s1) var(--s2);
    border-bottom: var(--line);
  }
  th {
    color: var(--muted);
    font-weight: 400;
    font-size: var(--fs-s);
  }
  .acts {
    display: flex;
    gap: var(--s2);
    align-items: center;
  }
  .acts button {
    padding: 0 var(--s2);
  }
  .banrow form {
    display: flex;
    gap: var(--s2);
    align-items: center;
  }
  .banrow input:first-child {
    flex: 1;
  }
  .payments {
    margin-top: var(--s4);
  }
  .pager {
    display: flex;
    gap: var(--s2);
    align-items: center;
    margin-top: var(--s2);
  }
  .row {
    display: flex;
    justify-content: space-between;
    gap: var(--s2);
    padding: 2px 0;
    overflow-wrap: anywhere;
  }
  h3 {
    font-size: var(--fs-s);
    color: var(--muted);
    margin: var(--s3) 0 var(--s1);
  }
  .distribution {
    display: flex;
    height: 8px;
    background: var(--surface-2);
  }
  .distribution span {
    flex-basis: 0;
  }
  .segments {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto auto;
    gap: var(--s1) var(--s2);
    margin: var(--s2) 0 0;
    padding: 0;
    list-style: none;
    font-size: var(--fs-s);
  }
  .segments li {
    display: grid;
    grid-column: 1 / -1;
    grid-template-columns: subgrid;
    align-items: center;
  }
  .segments .num {
    text-align: right;
  }
  .swatch {
    width: 8px;
    height: 8px;
  }
  .note {
    font-size: var(--fs-s);
    margin: var(--s2) 0;
  }
  details {
    margin-top: var(--s2);
    border-top: var(--line);
    padding-top: var(--s2);
  }
  summary {
    color: var(--muted);
    cursor: pointer;
  }

  @media (max-width: 900px) {
    .layout {
      grid-template-columns: minmax(0, 1fr);
      grid-template-areas: 'audience' 'users';
    }
  }
  @media (max-width: 700px) {
    .filters {
      flex-wrap: wrap;
    }
  }
</style>
