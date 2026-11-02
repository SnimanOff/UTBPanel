<script lang="ts">
  import { api, errorText, type Schemas } from '../lib/api/client'
  import { bytes, utc } from '../lib/format'
  import Box from '../lib/ui/Box.svelte'
  type Settings = Schemas['FullBackupSettings']
  let status = $state<Schemas['FullBackupStatus']>()
  let draft = $state<Settings>({ enabled: false, cron: '0 3 * * *', keep: 7, files: false })
  let dirty = $state(false), busy = $state(false), saving = $state(false)
  let error = $state(''), message = $state('')
  let generation = 0
  async function load() {
    const current = ++generation
    try { const value = await api.get<Schemas['FullBackupStatus']>('/api/backups'); if (current !== generation) return; status = value; if (!dirty) draft = { ...value.settings } }
    catch (e) { error = errorText(e) }
  }
  $effect(() => { load(); const timer = setInterval(load, 15_000); return () => clearInterval(timer) })
  async function save() {
    saving = true; generation++; error = ''; message = ''
    try { await api.put('/api/backups/settings', draft); dirty = false; message = 'Расписание и хранение сохранены'; await load() }
    catch (e) { error = errorText(e) }
    finally { saving = false }
  }
  async function make() {
    busy = true; error = ''; message = ''
    try { await api.post('/api/backups', { files: draft.files }); message = 'Полный бэкап создан'; await load() }
    catch (e) { error = errorText(e) }
    finally { busy = false }
  }
</script>

<Box title="Полный бэкап">
  <p class="muted">Настройки, секреты, пользователи, базы ботов и ключи устройств. Архив зашифрован мастер-ключом ядра. Для восстановления сохраните master.key отдельно через SSH.</p>
  <div class="settings">
    <label><input type="checkbox" bind:checked={draft.enabled} onchange={() => dirty = true} disabled={saving} /> По расписанию</label>
    <label>Расписание UTC<input class="mono" bind:value={draft.cron} oninput={() => dirty = true} disabled={saving} placeholder="0 3 * * *" /></label>
    <label>Хранить последних<input type="number" min="1" max="100" bind:value={draft.keep} oninput={() => dirty = true} disabled={saving} /></label>
    <label><input type="checkbox" bind:checked={draft.files} onchange={() => dirty = true} disabled={saving} /> Добавлять DATA_DIR</label>
    <button onclick={save} disabled={!dirty || saving}>Сохранить</button>
    <button class="primary" onclick={make} disabled={busy || status?.busy}>{busy || status?.busy ? 'Создание бэкапа…' : 'Создать бэкап'}</button>
  </div>
  <p class="muted small">Пять полей cron: 0 3 * * * — каждый день в 03:00 UTC. При превышении лимита старые архивы удаляются.</p>
  {#if status?.next}<p class="muted">Следующий: {utc(status.next)}</p>{/if}
  {#if status?.last_download}<p class="muted">Последнее скачивание: {utc(status.last_download)}</p>{/if}
  {#if status?.last_error}<p class="danger">Последняя ошибка: {status.last_error}</p>{/if}
  {#each status?.files ?? [] as file (file.id)}
    <div class="file">
      <div class="file-info"><a href={`/api/backups/${file.id}`} download={file.name}>{file.name}</a><span>{bytes(file.size)}</span><span class="muted">{utc(file.created)}</span>{#if file.downloaded_at}<span class="muted">Скачан: {utc(file.downloaded_at)}</span>{/if}</div>
      {#if file.verification}
        <p class:danger={file.verification.state === 'failed'} class="small">
          {file.verification.state === 'passed' ? 'Проверен, всё в порядке' : file.verification.state === 'skipped' ? 'Проверка пропущена' : 'Ошибка проверки'} · {utc(file.verification.time)}{file.verification.error ? `: ${file.verification.error}` : ''}
        </p>
      {:else}<p class="muted small">{status?.busy ? 'Ожидает проверки…' : 'Архив ещё не проверен'}</p>{/if}
    </div>
  {:else}<p class="muted">Полных архивов пока нет.</p>{/each}
  {#if error}<p class="danger">{error}</p>{/if}
  {#if message}<p>{message}</p>{/if}
</Box>

<style>
  .settings { display: flex; gap: var(--s3); align-items: end; flex-wrap: wrap; }
  label > input:not([type=checkbox]) { display: block; margin-top: var(--s1); }
  input[type=number] { width: 110px; }
  .file { padding: var(--s2) 0; border-bottom: var(--line); }
  .file-info { display: flex; gap: var(--s3); flex-wrap: wrap; }
  .file p { margin-bottom: 0; }
  .small { font-size: var(--fs-s); }
</style>
