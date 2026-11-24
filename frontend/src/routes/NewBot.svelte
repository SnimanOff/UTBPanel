<script lang="ts">
  // "+ Бот": name, group, token. Saving checks the token with getMe; on an
  // error Telegram's answer is shown under the field and nothing is saved.
  import { api, ApiError, errorText, type Schemas } from '../lib/api/client'
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
  const nameError = $derived([...name].length > 64 ? 'Имя должно содержать не больше 64 символов' : '')

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
    if (saving || nameError) return
    saving = true
    tokenError = error = ''
    const group = groupId
    const controller = new AbortController()
    // Allow the core's 60-second Telegram request to return its error first.
    const timeout = setTimeout(() => controller.abort(), 70_000)
    try {
      const { group_assignment_failed: groupFailed, ...b } = await api.post<Schemas['BotCreated']>(
        '/api/bots', { name, token, group_id: group }, controller.signal,
      )
      store.put({
        ...b,
        group_id: groupFailed ? undefined : group || undefined,
        position: 0,
        status: { running: false, restarts: 0, last_exit: 0 },
      })
      close()
      router.go(`/bots/${b.id}/settings${groupFailed ? '?group_assignment_failed=1' : ''}`)
    } catch (err) {
      if (controller.signal.aborted) {
        error = 'Панель не ответила вовремя. Проверьте список ботов перед повторной попыткой.'
      } else if (err instanceof ApiError && err.code === 'telegram') {
        const reply = err.telegram as { error_code?: number; parameters?: { retry_after?: number } } | undefined
        if (reply?.error_code === 429) {
          const seconds = reply.parameters?.retry_after
          tokenError = typeof seconds === 'number' && seconds > 0
            ? `Telegram пока не смог проверить токен (429). Повторите попытку не раньше чем через ${seconds} с.`
            : 'Telegram пока не смог проверить токен (429). Повторите попытку позже.'
        } else {
          tokenError = 'Telegram: ' + err.message
        }
      } else {
        error = errorText(err)
      }
    } finally {
      clearTimeout(timeout)
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
      <Field label="Имя в панели" error={nameError}>
        <input bind:this={nameInput} bind:value={name} required aria-invalid={!!nameError} />
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
      <button class="primary" disabled={saving || !name.trim() || !!nameError || !token}>{saving ? 'Проверяю…' : 'Сохранить'}</button>
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
