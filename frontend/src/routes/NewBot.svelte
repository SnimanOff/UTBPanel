<script lang="ts">
  // "+ Бот": name, group, token. Saving checks the token with getMe; on an
  // error Telegram's answer is shown under the field and nothing is saved.
  import { api, ApiError, errorText, type Bot } from '../lib/api/client'
  import { store } from '../lib/api/stores.svelte'
  import { router } from '../lib/router.svelte'
  import Field from '../lib/ui/Field.svelte'
  import { fadeSlide } from '../lib/ui/motion'
  import { onMount } from 'svelte'

  let { onclose }: { onclose: () => void } = $props()
  let name = $state('')
  let token = $state('')
  let groupId = $state(0)
  let tokenError = $state('')
  let error = $state('')
  let saving = $state(false)
  let nameInput = $state<HTMLInputElement>()
  let dialog = $state<HTMLDialogElement>()

  onMount(() => {
    dialog?.showModal()
    nameInput?.focus()
  })

  function close() {
    dialog?.close()
    onclose()
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    saving = true
    tokenError = error = ''
    try {
      const b = await api.post<Bot>('/api/bots', { name, token, group_id: groupId })
      await store.load()
      close()
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

<dialog
  class="dialog"
  aria-labelledby="newbot-title"
  bind:this={dialog}
  onclick={(e) => e.target === dialog && close()}
  oncancel={(e) => {
    e.preventDefault()
    close()
  }}
  in:fadeSlide|global={{ y: 12 }}
>
  <form onsubmit={save}>
    <div class="fields">
      <h1 id="newbot-title">Новый бот</h1>
      <Field label="Имя в панели">
        <input bind:this={nameInput} bind:value={name} required maxlength="64" />
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
    </div>
    <div class="buttons">
      <button type="button" onclick={close}>Отмена</button>
      <button class="primary" disabled={saving || !name || !token}>{saving ? 'Проверяю…' : 'Сохранить'}</button>
    </div>
  </form>
</dialog>

<style>
  dialog::backdrop {
    background: rgb(12 10 15 / 0.7);
  }
  .dialog {
    --dialog-top: clamp(12px, 10dvh, 80px);
    position: fixed;
    inset: var(--dialog-top) 0 auto;
    width: min(440px, calc(100% - 2 * var(--s3)));
    max-height: none;
    color: var(--text);
    background: var(--surface);
    border: var(--line);
    padding: 0;
  }
  form {
    display: flex;
    flex-direction: column;
    max-height: calc(100dvh - var(--dialog-top) - var(--s3) - 2px);
  }
  .fields {
    overflow-y: auto;
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
    flex-wrap: wrap;
    gap: var(--s2);
    padding: var(--s2) var(--s4) var(--s4);
    flex: none;
  }
</style>
