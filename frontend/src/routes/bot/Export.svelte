<script lang="ts">
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import { store } from '../../lib/api/stores.svelte'
  import { bytes, utc } from '../../lib/format'
  import Box from '../../lib/ui/Box.svelte'
  import HoldButton from '../../lib/ui/HoldButton.svelte'
  let { botIds, all = false }: { botIds?: number[]; all?: boolean } = $props()
  let open = $state(false)
  let busy = $state(false)
  let files = $state<Schemas['ExportFile'][]>([])
  let password = $state('')
  let custom = $state('')
  let includeFiles = $state(false)
  let includeMetrics = $state(false)
  let allVersions = $state(false)
  let error = $state('')
  let stopped = $state<number[]>([])
  let downloaded = $state<number[]>([])
  let generation = 0
  const botKey = $derived((botIds ?? []).join(','))
  $effect(() => { void botKey; generation++; open = false; files = []; password = ''; custom = ''; error = ''; stopped = []; downloaded = []; busy = false })
  async function make() {
    const current = ++generation
    busy = true; error = ''; password = ''; files = []; downloaded = []; stopped = []
    try {
      const out = await api.post<Schemas['BotExportResult']>('/api/exports/bots', { bot_ids: botIds ?? [], password: custom, files: includeFiles, metrics: includeMetrics, all_versions: allVersions })
      if (current !== generation) return
      files = out.files; password = out.password; custom = ''
    } catch (e) { if (current === generation) error = errorText(e) }
    finally { if (current === generation) busy = false }
  }
  async function stop(id: number) {
    try { await api.post(`/api/bots/${id}/stop`); stopped = [...stopped, id]; store.refresh(id) }
    catch (e) { error = errorText(e) }
  }
  async function copy() {
    try { await navigator.clipboard.writeText(password) }
    catch { error = 'Выделите пароль и скопируйте вручную' }
  }
</script>

<div class="export">
  <button disabled={busy} onclick={() => open = !open}>{open ? 'Закрыть экспорт' : all ? 'Экспорт всех ботов' : 'Экспорт бота'}</button>
  {#if open}
    <Box title={all ? 'Экспорт всех ботов' : 'Перенести бота'}>
      <p class="muted">Зашифрованный архив .tgbot: настройки, бинарник, снимок базы, пользователи и платежи. Значения секретов и токен потребуется задать при импорте.</p>
      <div class="options">
        <label><input type="checkbox" bind:checked={includeFiles} disabled={busy} /> Файлы DATA_DIR</label>
        <label><input type="checkbox" bind:checked={includeMetrics} disabled={busy} /> История метрик</label>
        <label><input type="checkbox" bind:checked={allVersions} disabled={busy} /> Все сохранённые версии</label>
      </div>
      <label>Свой пароль (необязательно)<input type="password" autocomplete="new-password" bind:value={custom} disabled={busy} placeholder="Пусто — создать пароль из 6 слов" /></label>
      <button class="primary" disabled={busy || (custom.length > 0 && Array.from(custom).length < 10)} onclick={make}>{busy ? 'Сборка архивов…' : 'Создать архив'}</button>
      {#if password}
        <div class="password"><strong>Сохраните пароль: он показан только здесь{all ? ' и одинаков для всех архивов' : ''}.</strong><div><code>{password}</code><button onclick={copy}>Копировать пароль</button></div></div>
        {#each files as file (file.id)}
          <div class="file">
            <a href={`/api/bots/${file.bot_id}/exports/${file.id}`} download={file.name} onclick={() => downloaded = [...downloaded, file.bot_id]}>{file.name} · {bytes(file.size)}</a>
            <span class="muted">Доступен до {utc(file.expires)}</span>
            {#if downloaded.includes(file.bot_id)}
              {#if stopped.includes(file.bot_id)}<span>Бот остановлен</span>
              {:else}<span class="muted">После переноса можно остановить бота здесь:</span><HoldButton onconfirm={() => stop(file.bot_id)}>Остановить на этом сервере</HoldButton>{/if}
            {/if}
          </div>
        {/each}
      {/if}
      {#if error}<p class="danger">{error}</p>{/if}
    </Box>
  {/if}
</div>

<style>
  .export { margin-bottom: var(--s3); }
  .options { display: flex; flex-wrap: wrap; gap: var(--s3); margin: var(--s3) 0; }
  input[type=password] { display: block; width: min(100%, 420px); margin: var(--s2) 0; }
  .password { padding: var(--s3); margin-top: var(--s3); border: 1px solid var(--accent); }
  .password div { display: flex; align-items: center; gap: var(--s3); flex-wrap: wrap; margin-top: var(--s2); }
  code { user-select: all; overflow-wrap: anywhere; }
  .file { display: flex; gap: var(--s3); align-items: center; flex-wrap: wrap; padding: var(--s3) 0; border-bottom: var(--line); }
</style>
