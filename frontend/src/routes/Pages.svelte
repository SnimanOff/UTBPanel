<script lang="ts">
  // Static pages: one HTML file (CSS and JS inline) per subdomain, served by
  // the edge at https://<sub>.<DOMAIN>:<webhook port>/.
  import { api, errorText, type Schemas } from '../lib/api/client'
  import { bytes } from '../lib/format'
  import Box from '../lib/ui/Box.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'

  type Info = Schemas['PageInfo']
  type Page = Schemas['Page']
  const MAX = 512 * 1024

  let pages = $state<Info[]>([])
  let error = $state('')
  let busy = $state(false)
  // The editor: old = label of the page being edited, "" for a new one.
  let editing = $state<{ old: string; sub: string; html: string } | null>(null)
  const size = $derived(editing ? new TextEncoder().encode(editing.html).length : 0)

  async function load() {
    try { pages = await api.get<Info[]>('/api/pages') } catch (e) { error = errorText(e) }
  }
  $effect(() => { load() })

  async function edit(sub: string) {
    error = ''
    try {
      const p = await api.get<Page>(`/api/pages/${encodeURIComponent(sub)}`)
      editing = { old: p.sub, sub: p.sub, html: p.html }
    } catch (e) { error = errorText(e) }
  }

  async function pick(e: Event) {
    const f = (e.currentTarget as HTMLInputElement).files?.[0]
    if (!f || !editing) return
    if (f.size > MAX) { error = 'Файл больше 512 КБ'; return }
    editing.html = await f.text()
    if (!editing.sub) editing.sub = f.name.replace(/\.html?$/i, '').toLowerCase().replace(/[^a-z0-9-]/g, '-')
  }

  async function save() {
    if (!editing) return
    busy = true; error = ''
    try {
      const path = encodeURIComponent(editing.old || editing.sub.trim())
      await api.put<Info>(`/api/pages/${path}`, { sub: editing.sub.trim(), html: editing.html })
      editing = null
      await load()
    } catch (e) { error = errorText(e) } finally { busy = false }
  }

  async function remove(sub: string) {
    error = ''
    try { await api.del(`/api/pages/${encodeURIComponent(sub)}`); await load() } catch (e) { error = errorText(e) }
  }
</script>

<Box title="Страницы">
  {#snippet actions()}
    {#if !editing}<button onclick={() => (editing = { old: '', sub: '', html: '' })}>+ Страница</button>{/if}
  {/snippet}
  <p class="muted">Один HTML-файл на поддомене: стили и скрипты внутри файла, до 512 КБ. Нужен DOMAIN в .env и A-запись поддомена (или *.домен) на этот сервер.</p>
  {#if pages.length}
    <table>
      <tbody>
        {#each pages as p (p.sub)}
          <tr>
            <td><a href={p.url} target="_blank" rel="noopener" class="mono">{p.url.replace('https://', '')}</a></td>
            <td class="num muted">{bytes(p.size)}</td>
            <td class="num muted">{new Date(p.updated_at).toLocaleString()}</td>
            <td class="right">
              <button onclick={() => edit(p.sub)} disabled={busy}>Изменить</button>
              <HoldButton onconfirm={() => remove(p.sub)}>Удалить</HoldButton>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else if !editing}
    <p class="muted">Страниц нет.</p>
  {/if}
  {#if editing}
    <div class="editor">
      <div class="row">
        <input class="mono" placeholder="поддомен, например test" bind:value={editing.sub} disabled={busy} />
        <label class="file">Загрузить .html <input type="file" accept=".html,.htm,text/html" onchange={pick} /></label>
        <span class="num" class:danger={size > MAX}>{bytes(size)} / 512 КБ</span>
      </div>
      <textarea class="mono" rows="18" spellcheck="false" placeholder="<!doctype html>…" bind:value={editing.html} disabled={busy}></textarea>
      <div class="row">
        <button class="primary" onclick={save} disabled={busy || !editing.sub.trim() || !editing.html.trim() || size > MAX}>Сохранить</button>
        <button onclick={() => (editing = null)} disabled={busy}>Отмена</button>
      </div>
    </div>
  {/if}
  {#if error}<p class="danger">{error}</p>{/if}
</Box>

<style>
  table { width: 100%; border-collapse: collapse; }
  td { padding: var(--s1) var(--s2) var(--s1) 0; }
  .right { text-align: right; white-space: nowrap; }
  .editor { display: flex; flex-direction: column; gap: var(--s2); margin-top: var(--s2); }
  .row { display: flex; gap: var(--s2); align-items: center; flex-wrap: wrap; }
  textarea { width: 100%; box-sizing: border-box; resize: vertical; }
  .file { cursor: pointer; text-decoration: underline; }
  .file input { display: none; }
</style>
