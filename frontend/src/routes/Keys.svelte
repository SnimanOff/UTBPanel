<script lang="ts">
  // Keys: issue child keys (gate for Windows, Linux or macOS), approve new devices by code,
  // list with last seen and IP, revoke. A child key only sees the list.
  import { api, errorText } from '../lib/api/client'
  import { utc } from '../lib/format'
  import Box from '../lib/ui/Box.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'

  type Key = {
    serial: string
    name: string
    kind: string
    grants?: string
    device?: string
    created_at: string
    last_seen?: string
    last_ip?: string
    revoked_at?: string
    used_at?: string
    expires_at?: string
  }
  type Device = { id: number; name: string; kind: string; ip: string; code: string; created_at: string }

  let keys = $state<Key[]>([])
  let devices = $state<Device[]>([])
  let me = $state<{ pong: string; kind: string }>()
  let name = $state('')
  let os = $state('windows')
  const systems = [
    ['windows', 'Windows'],
    ['linux', 'Linux'],
    ['macos', 'macOS (Apple)'],
    ['macos-intel', 'macOS (Intel)'],
  ]
  let error = $state('')
  let issuing = $state(false)

  async function load() {
    try {
      ;[keys, devices, me] = await Promise.all([
        api.get<Key[]>('/api/keys'),
        api.get<Device[]>('/api/devices'),
        api.get<{ pong: string; kind: string }>('/api/ping'),
      ])
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    load()
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  })

  const owner = $derived(me?.kind === 'owner')
  const active = $derived(keys.filter((k) => k.kind !== 'enrollment' && !k.revoked_at))
  const waiting = $derived(keys.filter((k) => k.kind === 'enrollment' && !k.used_at && !k.revoked_at && (!k.expires_at || new Date(k.expires_at) > new Date())))
  const revoked = $derived(keys.filter((k) => k.kind !== 'enrollment' && k.revoked_at))

  async function issue(e: SubmitEvent) {
    e.preventDefault()
    issuing = true
    error = ''
    try {
      const resp = await fetch('/api/keys', {
        method: 'POST',
        headers: { 'X-Panel': '1', 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, os }),
      })
      if (!resp.ok) {
        const b = await resp.json().catch(() => ({ message: resp.statusText }))
        throw new Error(b.message)
      }
      const blob = await resp.blob()
      const a = document.createElement('a')
      a.href = URL.createObjectURL(blob)
      a.download = os === 'windows' ? `gate-${name}.exe` : `gate-${name}`
      a.click()
      URL.revokeObjectURL(a.href)
      name = ''
      load()
    } catch (err) {
      error = errorText(err)
    } finally {
      issuing = false
    }
  }

  async function act(fn: () => Promise<unknown>) {
    error = ''
    try {
      await fn()
    } catch (e) {
      error = errorText(e)
    }
    load()
  }
</script>

{#if error}<p class="danger">{error}</p>{/if}

{#if devices.length}
  <Box title="Новые устройства" problem>
    {#each devices as d (d.id)}
      <div class="device">
        <div>
          <div><b>{d.name}</b> <span class="muted">· {d.kind === 'owner' ? 'ключ владельца' : 'дочерний ключ'} · {d.ip} · {utc(d.created_at)}</span></div>
          <div class="code mono">{d.code}</div>
          <div class="muted small">Сверьте код с окном gate на новом устройстве.</div>
        </div>
        {#if owner}
          <div class="actions">
            <HoldButton danger={false} onconfirm={() => act(() => api.post(`/api/devices/${d.id}/approve`))}>Подтвердить</HoldButton>
            <button onclick={() => act(() => api.post(`/api/devices/${d.id}/reject`))}>Отклонить</button>
          </div>
        {/if}
      </div>
    {/each}
  </Box>
{/if}

<div class="top">
  <h1>Ключи</h1>
  {#if owner}
    <form onsubmit={issue}>
      <input bind:value={name} placeholder="имя устройства, например laptop" pattern="[A-Za-z0-9][A-Za-z0-9_.\-]*" maxlength="40" required />
      <select bind:value={os} title="Система устройства">
        {#each systems as [value, label] (value)}<option {value}>{label}</option>{/each}
      </select>
      <button class="primary" disabled={issuing || !name}>Новый ключ</button>
    </form>
  {:else}
    <span class="muted">Дочерний ключ видит только список.</span>
  {/if}
</div>

<table>
  <thead>
    <tr><th>Имя</th><th>Вид</th><th>Устройство</th><th>Последний вход</th><th>IP</th><th>Создан</th><th></th></tr>
  </thead>
  <tbody>
    {#each active as k (k.serial)}
      <tr>
        <td>{k.name}</td>
        <td>{k.kind === 'owner' ? 'владелец' : 'дочерний'}</td>
        <td class="muted">{k.device || '—'}</td>
        <td class="num">{utc(k.last_seen)}</td>
        <td class="num">{k.last_ip || '—'}</td>
        <td class="num muted">{utc(k.created_at)}</td>
        <td>
          {#if owner && k.kind === 'child'}
            <HoldButton onconfirm={() => act(() => api.post(`/api/keys/${k.serial}/revoke`))}>Отозвать</HoldButton>
          {:else if k.kind === 'owner'}
            <span class="muted small" title="Только по SSH: panel keys revoke">отзыв по SSH</span>
          {/if}
        </td>
      </tr>
    {/each}
    {#each waiting as k (k.serial)}
      <tr class="muted">
        <td>{k.name}</td>
        <td>{k.grants === 'owner' ? 'владелец' : 'дочерний'}</td>
        <td colspan="3">ждёт регистрации до {utc(k.expires_at)}</td>
        <td class="num">{utc(k.created_at)}</td>
        <td>
          {#if owner}
            <HoldButton onconfirm={() => act(() => api.post(`/api/keys/${k.serial}/revoke`))}>Отменить</HoldButton>
          {/if}
        </td>
      </tr>
    {/each}
  </tbody>
</table>

{#if revoked.length}
  <h2 class="revoked">Отозванные</h2>
  <table>
    <tbody>
      {#each revoked as k (k.serial)}
        <tr class="muted"><td>{k.name}</td><td>{k.kind}</td><td class="num">отозван {utc(k.revoked_at)}</td><td class="num">{k.last_ip || ''}</td></tr>
      {/each}
    </tbody>
  </table>
{/if}

<style>
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin: var(--s4) 0 var(--s3);
  }
  form {
    display: flex;
    gap: var(--s2);
  }
  form input {
    width: 280px;
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
  .device {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--s4);
    padding: var(--s2) 0;
  }
  .code {
    font-size: 22px;
    margin: var(--s2) 0;
    letter-spacing: 0.5px;
  }
  .actions {
    display: flex;
    gap: var(--s2);
  }
  .small {
    font-size: var(--fs-s);
  }
  .revoked {
    margin: var(--s5) 0 var(--s2);
  }

  /* Phones: one column, rows of controls wrap. */
  @media (max-width: 700px) {
    form {
      flex-wrap: wrap;
    }
    form input {
      width: 100%;
    }
    .top {
      flex-wrap: wrap;
    }
  }
</style>
