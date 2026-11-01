<script lang="ts">
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import Box from '../../lib/ui/Box.svelte'
  let { botId }: { botId: number } = $props()
  type Flag = Schemas['SDKFlag']
  let flags = $state<Flag[]>([])
  let drafts = $state<Record<string, string>>({})
  let dirty = $state<Record<string, boolean>>({})
  let error = $state('')
  let busy = $state(false)
  let revision = 0
  async function load(force = false) {
    if (busy && !force) return
    const current = ++revision, id = botId
    try {
      const values = await api.get<Flag[]>(`/api/bots/${id}/sdk/flags`)
      if (current !== revision || id !== botId) return
      flags = values
      for (const f of values) if (!dirty[f.name]) drafts[f.name] = String(f.value)
      error = ''
    } catch (e) { if (current === revision) error = errorText(e) }
  }
  $effect(() => { void botId; load(); const timer = setInterval(() => load(), 5000); return () => { clearInterval(timer); revision++ } })
  async function save(f: Flag, value: Flag['value'], reset = false) {
    busy = true; revision++
    try {
      const path = `/api/bots/${botId}/sdk/flags/${encodeURIComponent(f.name)}`
      if (reset) await api.del(path)
      else await api.put(path, { value })
      dirty[f.name] = false
      await load(true)
    } catch (e) { error = errorText(e) }
    finally { busy = false }
  }
  function inputValue(f: Flag): Flag['value'] { return f.kind === 'int' ? Number(drafts[f.name]) : drafts[f.name] }
</script>

<Box title="Переключатели">
  <p class="muted">Изменения применяются сразу, без рестарта бота.</p>
  {#if error}<p class="danger">{error}</p>{/if}
  {#each flags as f (f.name)}
    <div class="flag">
      <div><strong>{f.name}</strong><p class="muted">{f.description}</p><small class="muted">{f.kind === 'duration' ? 'Длительность' : f.kind === 'int' ? 'Число' : f.kind === 'string' ? 'Строка' : 'Переключатель'} · По умолчанию: {String(f.default)}</small></div>
      <div class="controls">
        {#if f.kind === 'bool'}
          <label><input type="checkbox" checked={!!f.value} disabled={busy} onchange={(e) => save(f, e.currentTarget.checked)} /> {f.value ? 'Включено' : 'Выключено'}</label>
        {:else}
          <input type={f.kind === 'int' ? 'number' : 'text'} step="1" aria-label={f.name} disabled={busy} value={drafts[f.name] ?? ''} oninput={(e) => { drafts[f.name] = e.currentTarget.value; dirty[f.name] = true }} />
          <button disabled={busy || !dirty[f.name]} onclick={() => save(f, inputValue(f))}>Сохранить</button>
        {/if}
        {#if f.overridden}<button disabled={busy} onclick={() => save(f, f.default, true)}>По умолчанию</button>{/if}
      </div>
    </div>
  {:else}<p class="muted">Переключатели не объявлены</p>{/each}
</Box>

<style>
  .flag { display: flex; justify-content: space-between; gap: var(--s3); padding: var(--s3) 0; border-bottom: var(--line); }
  .flag p { margin: var(--s1) 0; }
  .controls { display: flex; align-items: center; gap: var(--s2); }
  .controls input:not([type=checkbox]) { width: 180px; }
  @media (max-width: 700px) { .flag { flex-direction: column; } .controls { flex-wrap: wrap; } }
</style>
