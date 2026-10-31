<script lang="ts">
  // "+ Бот": name, group, token. Saving checks the token with getMe; on an
  // error Telegram's answer is shown under the field and nothing is saved.
  import { api, ApiError, errorText, type Bot } from '../lib/api/client'
  import { store } from '../lib/api/stores.svelte'
  import { router } from '../lib/router.svelte'
  import Field from '../lib/ui/Field.svelte'

  let { onclose }: { onclose: () => void } = $props()
  let name = $state('')
  let token = $state('')
  let groupId = $state(0)
  let tokenError = $state('')
  let error = $state('')
  let saving = $state(false)

  async function save(e: SubmitEvent) {
    e.preventDefault()
    saving = true
    tokenError = error = ''
    try {
      const b = await api.post<Bot>('/api/bots', { name, token, group_id: groupId })
      await store.load()
      onclose()
      router.go(`/bots/${b.id}/settings`)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'telegram') {
        tokenError = 'Telegram: ' + err.message
      } else {
        error = errorText(err)
      }
    } finally {
      saving = false
    }
  }
</script>

<div class="backdrop" role="presentation" onclick={onclose}></div>
<form class="dialog" onsubmit={save}>
  <h1>Новый бот</h1>
  <Field label="Имя в панели">
    <input bind:value={name} required maxlength="64" />
  </Field>
  <Field label="Группа">
    <select bind:value={groupId}>
      <option value={0}>Без группы</option>
      {#each store.groups as g}<option value={g.id}>{g.name}</option>{/each}
    </select>
  </Field>
  <Field label="Токен от BotFather" error={tokenError} hint="После сохранения токен больше нигде не показывается">
    <input type="password" autocomplete="off" bind:value={token} required />
  </Field>
  {#if error}<p class="danger">{error}</p>{/if}
  <div class="buttons">
    <button type="button" onclick={onclose}>Отмена</button>
    <button class="primary" disabled={saving || !name || !token}>{saving ? 'Проверяю…' : 'Сохранить'}</button>
  </div>
</form>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgb(12 10 15 / 0.7);
    z-index: 10;
  }
  .dialog {
    position: fixed;
    z-index: 11;
    top: 80px;
    left: 50%;
    transform: translateX(-50%);
    width: min(440px, 92vw);
    background: var(--surface);
    border: var(--line);
    padding: var(--s4);
  }
  h1 {
    margin-bottom: var(--s4);
  }
  input,
  select {
    width: 100%;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: var(--s2);
  }
</style>
