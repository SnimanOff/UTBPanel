<script lang="ts">
  // The invitation page opened on a phone: create a passkey, show the four
  // words, wait until the owner approves, then go into the panel.
  import { api, errorText } from '../lib/api/client'
  import { create, passkeyError, supported } from '../lib/passkey'

  let { token }: { token: string } = $props()
  type Info = { name: string; role: string; role_name: string; all_bots: boolean; bots: number }
  let info = $state<Info>()
  let code = $state('')
  let phase = $state<'new' | 'waiting' | 'gone'>('new')
  let busy = $state(false)
  let error = $state('')

  $effect(() => {
    api.get<Info>(`/web/invite/${token}`).then((i) => (info = i), (e) => (error = errorText(e)))
  })

  async function enroll() {
    busy = true
    error = ''
    try {
      const options = await api.post<any>(`/web/invite/${token}/begin`)
      const reg = await create(options)
      const r = await api.post<{ code: string; poll: string }>(`/web/invite/${token}/finish`, reg)
      code = r.code
      phase = 'waiting'
      wait(r.poll)
    } catch (e) {
      error = e instanceof DOMException ? passkeyError(e) : errorText(e)
    } finally {
      busy = false
    }
  }

  function wait(poll: string) {
    const t = setInterval(async () => {
      try {
        const r = await api.get<{ state: string }>(`/web/pending/${poll}`)
        if (r.state === 'approved') {
          clearInterval(t)
          location.href = '/'
        } else if (r.state === 'gone') {
          clearInterval(t)
          phase = 'gone'
        }
      } catch {
        /* the next tick tries again */
      }
    }, 2000)
  }
</script>

<div class="center">
  <div class="card">
    <div class="logo mono">TG PANEL</div>
    {#if phase === 'waiting'}
      <p>Покажите эти слова владельцу панели или прочитайте вслух:</p>
      <p class="code mono">{code}</p>
      <p class="muted">Страница сама откроет панель, когда владелец подтвердит.</p>
    {:else if phase === 'gone'}
      <p class="danger">Владелец отклонил ключ или время вышло. Попросите новую ссылку.</p>
    {:else if info}
      <h1>Приглашение: {info.name}</h1>
      <p class="muted">
        {info.all_bots ? 'Все боты' : `Ботов: ${info.bots}`} · {info.role_name}
      </p>
      {#if supported()}
        <button class="primary big" onclick={enroll} disabled={busy}>{busy ? 'Создаём ключ…' : 'Создать ключ входа'}</button>
        <p class="muted">Ключ останется в защищённом чипе этого телефона. Вход — по Face ID, отпечатку или PIN.</p>
      {:else}
        <p class="danger">Этот браузер не умеет создавать passkey.</p>
      {/if}
    {/if}
    {#if error}<p class="danger">{error}</p>{/if}
  </div>
</div>

<style>
  .center { min-height: 100vh; display: grid; place-items: center; padding: var(--s4); }
  .card { width: 100%; max-width: 380px; display: flex; flex-direction: column; gap: var(--s3); text-align: center; }
  .logo { font-size: var(--fs-m); letter-spacing: 0.12em; border: 1px solid var(--accent); padding: 2px var(--s2); align-self: center; }
  .big { padding: var(--s3); font-size: var(--fs-m); }
  .code { font-size: 1.6rem; color: var(--accent-text); }
</style>
