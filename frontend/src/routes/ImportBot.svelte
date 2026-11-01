<script lang="ts">
  import { api, errorText, type Schemas } from '../lib/api/client'
  import { store } from '../lib/api/stores.svelte'
  import { utc } from '../lib/format'
  import Box from '../lib/ui/Box.svelte'
  let open = $state(false), busy = $state(false), error = $state('')
  let password = $state(''), token = $state(''), name = $state(''), start = $state(true)
  let input = $state<HTMLInputElement>()
  let preview = $state<Schemas['BotImportPreview']>()
  let result = $state<Schemas['BotImportResult']>()
  let values = $state<Record<string, { kind: string; value: string }>>({})
  let reading = $state(false)
  async function inspect(e: SubmitEvent) {
    e.preventDefault(); const file = input?.files?.[0]; if (!file) return
    busy = true; error = ''
    try {
      const body = new FormData(); body.append('password', password); body.append('file', file)
      preview = await api.post<Schemas['BotImportPreview']>('/api/imports/bots', body)
      name = preview.manifest.name; start = !!preview.manifest.current_version; password = ''
      values = Object.fromEntries(preview.document.secrets.map((name) => [name, { kind: 'value', value: '' }]))
    } catch (e) { error = errorText(e) }
    finally { busy = false }
  }
  async function secretFile(name: string, file?: File) {
    values[name].value = ''
    if (!file) return
    if (file.size > 1 << 20) { error = 'Файл секрета должен быть не больше 1 МБ'; return }
    reading = true
    try {
      const data = new Uint8Array(await file.arrayBuffer()); let binary = ''
      for (const value of data) binary += String.fromCharCode(value)
      values[name] = { kind: 'file', value: btoa(binary) }
    } catch (e) { error = errorText(e) }
    finally { reading = false }
  }
  async function apply(e: SubmitEvent) {
    e.preventDefault(); if (!preview) return
    busy = true; error = ''
    try {
      result = await api.post<Schemas['BotImportResult']>(`/api/imports/bots/${preview.id}/apply`, { name, token, secrets: values, start })
      await store.load()
      if (!result.warnings.length) { preview = undefined; token = ''; values = {} }
    } catch (e) { error = errorText(e) }
    finally { busy = false }
  }
  async function close() {
    if (preview) { try { await api.del(`/api/imports/bots/${preview.id}`) } catch (e) { error = errorText(e); return } }
    preview = undefined; result = undefined; token = password = ''; values = {}; open = false
  }
</script>

<div class="import">
  <button disabled={busy || reading} onclick={() => open ? close() : open = true}>{open ? 'Закрыть импорт' : 'Импорт бота'}</button>
  {#if open}
    <Box title="Импорт .tgbot">
      {#if !preview && !result}
        <form onsubmit={inspect}>
          <label>Архив<input type="file" accept=".tgbot" bind:this={input} required disabled={busy} /></label>
          <label>Пароль архива<input type="password" autocomplete="off" bind:value={password} required disabled={busy} /></label>
          <button class="primary" disabled={busy}>{busy ? 'Загрузка и проверка…' : 'Проверить архив'}</button>
        </form>
      {/if}
      {#if preview}
        <p>Архив @{preview.manifest.username || preview.manifest.name} проверен. Временные данные доступны до {utc(preview.expires)}.</p>
        <p class="muted">Введите токен этого бота и значения секретов: их нет в архиве. Бот получит новый локальный ID. При запуске вебхук переключится на этот сервер.</p>
        <form onsubmit={apply}>
          <label>Имя в панели<input bind:value={name} required disabled={busy} /></label>
          <label>BOT_TOKEN<input type="password" autocomplete="off" bind:value={token} required disabled={busy} /></label>
          {#each preview.document.secrets as secret (secret)}
            <div class="secret"><strong class="mono">{secret}</strong><select aria-label={`Тип ${secret}`} bind:value={values[secret].kind} disabled={busy || reading} onchange={() => values[secret].value = ''}><option value="value">Значение</option><option value="file">Файл</option></select>
              {#if values[secret].kind === 'file'}<input aria-label={secret} type="file" disabled={busy || reading} required={!values[secret].value} onchange={(e) => secretFile(secret, e.currentTarget.files?.[0])} />
              {:else}<input aria-label={secret} type="password" autocomplete="off" bind:value={values[secret].value} disabled={busy} required />{/if}
            </div>
          {/each}
          <label><input type="checkbox" bind:checked={start} disabled={busy || !preview.manifest.current_version} /> Запустить после импорта</label>
          <button class="primary" disabled={busy || reading || Object.values(values).some((v) => !v.value)}>{busy ? 'Восстановление…' : result ? 'Повторить настройки панели и запуск' : 'Импортировать'}</button>
        </form>
      {/if}
      {#if result}
        <p><a href={`/bots/${result.bot.id}`}>Бот {result.bot.name}</a> импортирован: {result.started ? 'запущен' : 'остановлен'}.</p>
        {#each result.warnings as warning}<p class="danger">{warning}</p>{/each}
      {/if}
      {#if error}<p class="danger">{error}</p>{/if}
    </Box>
  {/if}
</div>

<style>
  .import { margin-bottom: var(--s3); }
  form { display: grid; gap: var(--s3); max-width: 680px; }
  label > input:not([type=checkbox]) { display: block; width: 100%; margin-top: var(--s1); }
  .secret { display: grid; grid-template-columns: 1fr auto; gap: var(--s2); }
  .secret > input { grid-column: 1 / -1; }
</style>
