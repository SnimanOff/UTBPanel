<script lang="ts">
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import Box from '../../lib/ui/Box.svelte'
  let { botId, onapplied }: { botId: number; onapplied: (restart: boolean) => void } = $props()
  let open = $state(false)
  let text = $state('')
  let version = $state('')
  let busy = $state(false)
  let error = $state('')
  let message = $state('')
  let diff = $state<Schemas['BotConfigDiff']>()
  let reviewed = $state('')
  let requestID = 0
  $effect(() => { void botId; open = false; busy = false; text = ''; version = ''; diff = undefined; error = ''; message = ''; requestID++ })
  async function load() {
    const current = ++requestID, id = botId
    open = true; busy = true; error = ''; message = ''; diff = undefined
    try {
      const out = await api.get<Schemas['BotConfigView']>(`/api/bots/${id}/config`)
      if (current !== requestID || id !== botId) return
      text = out.yaml; version = out.document.version ?? ''
    } catch (e) { if (current === requestID) error = errorText(e) }
    finally { if (current === requestID) busy = false }
  }
  async function copy() {
    try { await navigator.clipboard.writeText(text); message = 'Документ скопирован' }
    catch { error = 'Не удалось скопировать. Выделите текст и нажмите Ctrl+C.' }
  }
  async function preview() {
    const current = ++requestID, id = botId, draft = text
    busy = true; error = ''; message = ''; diff = undefined
    try {
      const out = await api.post<Schemas['BotConfigDiff']>(`/api/bots/${id}/config/diff`, { version, yaml: draft })
      if (current !== requestID || id !== botId) return
      diff = out; reviewed = draft
      if (!out.changes.length) message = 'Изменений нет'
    } catch (e) { if (current === requestID) error = errorText(e) }
    finally { if (current === requestID) busy = false }
  }
  async function apply() {
    if (reviewed !== text) return
    const current = ++requestID, id = botId
    busy = true; error = ''; message = ''
    try {
      const out = await api.post<Schemas['BotConfigResult']>(`/api/bots/${id}/config/apply`, { version, yaml: text })
      if (current !== requestID || id !== botId) return
      text = out.yaml; version = out.document.version ?? ''; diff = undefined
      if (out.applied) {
        message = out.missing_secrets.length ? `Настройки сохранены. Задайте значения секретов: ${out.missing_secrets.join(', ')}` : 'Настройки сохранены'
        onapplied(out.restart)
      } else error = 'Telegram применил только часть профиля: ' + Object.entries(out.profile_errors).map(([field, msg]) => `${field}: ${msg}`).join('; ')
    } catch (e) { if (current === requestID) error = errorText(e) }
    finally { if (current === requestID) busy = false }
  }
</script>

<div class="document">
  <button disabled={busy} onclick={() => open ? open = false : load()}>{open ? 'Закрыть документ' : 'Как документ'}</button>
  {#if open}
    <Box title="Настройки как документ">
      <p class="muted">YAML с настройками и именами секретов. Значения секретов и токен сюда не входят. Удаление имени секрета удалит его значение. Группа доступна только для чтения.</p>
      <label for="config-yaml" class="muted">Документ настроек</label>
      <textarea id="config-yaml" class="mono" spellcheck="false" bind:value={text} disabled={busy} oninput={() => { diff = undefined; message = '' }}></textarea>
      <div class="actions">
        <button disabled={busy || !text} onclick={copy}>Копировать</button>
        <button disabled={busy} onclick={load}>Перечитать</button>
        <button disabled={busy || !text} onclick={preview}>Показать изменения</button>
      </div>
      {#if diff?.changes.length}
        <div class="changes">
          {#each diff.changes as change}
            <div class="change"><strong class="mono">{change.path}</strong><span class="muted">{change.level === 'confirm' ? 'С подтверждением' : 'Сразу'}</span><del class="mono">{JSON.stringify(change.before)}</del><ins class="mono">{JSON.stringify(change.after)}</ins></div>
          {/each}
        </div>
        {#if diff.restart}<p class="muted">Переменные и секреты вступят в силу после рестарта бота.</p>{/if}
        <button class="primary" disabled={busy || reviewed !== text} onclick={apply}>Применить изменения</button>
      {/if}
      {#if error}<p class="danger">{error}</p>{/if}
      {#if message}<p>{message}</p>{/if}
    </Box>
  {/if}
</div>

<style>
  .document { margin-bottom: var(--s3); }
  textarea { width: 100%; min-height: 480px; resize: vertical; margin: var(--s2) 0; }
  .actions { display: flex; gap: var(--s2); flex-wrap: wrap; }
  .changes { margin: var(--s3) 0; max-height: 400px; overflow: auto; }
  .change { display: grid; grid-template-columns: 1fr auto; gap: var(--s1) var(--s3); border-bottom: var(--line); padding: var(--s2) 0; }
  del, ins { grid-column: 1 / -1; white-space: pre-wrap; overflow-wrap: anywhere; text-decoration: none; }
  del { color: var(--danger); } ins { color: var(--accent-hi); }
</style>
