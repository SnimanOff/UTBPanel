<script lang="ts">
  // Sign-in on the web listener: one button, Face ID or fingerprint.
  import { api, errorText } from '../lib/api/client'
  import { get, passkeyError, supported } from '../lib/passkey'

  let busy = $state(false)
  let error = $state('')

  async function login() {
    busy = true
    error = ''
    try {
      const b = await api.post<{ challenge_id: string; public_key: any }>('/web/login/begin')
      const assertion = await get(b.public_key)
      await api.post('/web/login/finish', { challenge_id: b.challenge_id, ...assertion })
      location.href = '/'
    } catch (e) {
      error = e instanceof DOMException ? passkeyError(e) : errorText(e)
    } finally {
      busy = false
    }
  }
</script>

<div class="center">
  <div class="card">
    <div class="logo mono">TG PANEL</div>
    {#if !supported()}
      <p class="danger">Этот браузер не умеет входить по passkey.</p>
    {:else}
      <button class="primary big" onclick={login} disabled={busy}>{busy ? 'Ждём подтверждения…' : 'Войти'}</button>
      <p class="muted">Face ID, отпечаток или PIN телефона. Ключ входа выдаёт владелец панели.</p>
    {/if}
    {#if error}<p class="danger">{error}</p>{/if}
  </div>
</div>

<style>
  .center { min-height: 100vh; display: grid; place-items: center; padding: var(--s4); }
  .card { width: 100%; max-width: 360px; display: flex; flex-direction: column; gap: var(--s3); text-align: center; }
  .logo { font-size: var(--fs-m); letter-spacing: 0.12em; border: 1px solid var(--accent); padding: 2px var(--s2); align-self: center; }
  .big { padding: var(--s3); font-size: var(--fs-m); }
</style>
