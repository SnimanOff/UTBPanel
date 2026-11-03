<script lang="ts">
  // Broadcast: editor with the preview of Telegram formatting, a picture and
  // link buttons; audience with the number of recipients; "Отправить мне";
  // launch now or at a UTC time by holding the button. Below, the history
  // with live progress, pause and cancel.
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import { utc } from '../../lib/format'
  import { limit, render } from '../../lib/tgformat'
  import { defaultTZ, pair } from '../../lib/tz'
  import Box from '../../lib/ui/Box.svelte'
  import HoldButton from '../../lib/ui/HoldButton.svelte'

  let { botId }: { botId: number } = $props()
  type B = Schemas['Broadcast']
  type Btn = Schemas['LinkButton']

  let text = $state('')
  let mode = $state<'HTML' | 'MarkdownV2'>('HTML')
  let photo = $state<File | null>(null)
  let photoURL = $state('')
  let fileInput = $state<HTMLInputElement>()
  let buttons = $state<Btn[]>([])
  let audience = $state<'all' | 'active'>('all')
  let days = $state(30)
  let when = $state<'now' | 'at'>('now')
  let at = $state(new Date(Date.now() + 3600e3).toISOString().slice(0, 16))
  let estimate = $state<number | null>(null)
  let tz = $state(defaultTZ)
  let history = $state<B[]>([])
  let error = $state('')
  let info = $state('')
  let busy = $state(false)
  let preview = $state<HTMLElement>()

  $effect(() => {
    if (preview) render(preview, text, mode)
  })
  $effect(() => {
    const q = new URLSearchParams({ audience, days: String(days) })
    api.get<{ count: number }>(`/api/bots/${botId}/broadcasts/estimate?${q}`).then((r) => (estimate = r.count), () => (estimate = null))
  })
  $effect(() => {
    void botId
    api.get<Record<string, any>>(`/api/bots/${botId}/settings`).then((s) => (tz = s.tz || defaultTZ), () => {})
  })

  async function loadHistory() {
    try {
      history = await api.get<B[]>(`/api/bots/${botId}/broadcasts`)
    } catch (e) {
      error = errorText(e)
    }
  }
  const active = $derived(history.some((b) => b.state === 'running' || b.state === 'scheduled'))
  $effect(() => {
    void botId
    loadHistory()
  })
  // Live progress while something is being sent.
  $effect(() => {
    if (!active) return
    const t = setInterval(loadHistory, 1500)
    return () => clearInterval(t)
  })

  function pickPhoto() {
    const f = fileInput?.files?.[0] ?? null
    if (photoURL) URL.revokeObjectURL(photoURL)
    photo = f
    photoURL = f ? URL.createObjectURL(f) : ''
  }
  function dropPhoto() {
    photo = null
    if (photoURL) URL.revokeObjectURL(photoURL)
    photoURL = ''
    if (fileInput) fileInput.value = ''
  }

  function body(): FormData | object {
    const data: Record<string, unknown> = {
      text,
      parse_mode: mode,
      buttons: buttons.filter((b) => b.text.trim() && b.url.trim()),
      audience,
      active_days: audience === 'active' ? days : 0,
    }
    if (when === 'at') data.starts_at = new Date(at + ':00Z').toISOString()
    if (!photo) return data
    const fd = new FormData()
    fd.append('data', JSON.stringify(data))
    fd.append('photo', photo)
    return fd
  }

  async function run(fn: () => Promise<unknown>, msg: string) {
    busy = true
    error = ''
    info = ''
    try {
      await fn()
      info = msg
      setTimeout(() => (info = ''), 4000)
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
  const sendMe = () => run(() => api.post(`/api/bots/${botId}/broadcasts/test`, body()), 'Отправлено вам в Telegram')
  const launch = () =>
    run(async () => {
      await api.post(`/api/bots/${botId}/broadcasts`, body())
      text = ''
      buttons = []
      dropPhoto()
      await loadHistory()
    }, when === 'now' ? 'Рассылка запущена' : 'Рассылка запланирована')
  const control = (b: B, op: string) => run(async () => {
    await api.post(`/api/bots/${botId}/broadcasts/${b.id}/${op}`)
    await loadHistory()
  }, '')

  const max = $derived(limit(!!photo))
  const atDate = $derived(new Date(at + ':00Z'))
  const states: Record<string, string> = {
    scheduled: 'запланирована', running: 'идёт', paused: 'на паузе', done: 'готово', cancelled: 'отменена',
  }
  const done = (b: B) => b.sent + b.blocked + b.failed
  const pct = (b: B) => (b.total ? Math.min(100, Math.round((done(b) / b.total) * 100)) : b.state === 'done' ? 100 : 0)
</script>

<div class="cols">
  <Box title="Новая рассылка">
    <div class="modes">
      {#each ['HTML', 'MarkdownV2'] as m}
        <button class:active={mode === m} onclick={() => (mode = m as typeof mode)}>{m}</button>
      {/each}
      <span class="grow"></span>
      <span class="num" class:danger={text.length > max}>{text.length}/{max}</span>
    </div>
    <textarea rows="8" bind:value={text} placeholder={'<b>Новости</b>\nТекст рассылки…'}></textarea>
    <div class="line">
      <input type="file" accept="image/*" bind:this={fileInput} onchange={pickPhoto} />
      {#if photo}<button onclick={dropPhoto}>убрать картинку</button>{/if}
    </div>
    <div>
      {#each buttons as b, i}
        <div class="line">
          <input bind:value={b.text} placeholder="текст кнопки" />
          <input class="grow" bind:value={b.url} placeholder="https://…" />
          <button class="x" onclick={() => (buttons = buttons.filter((_, j) => j !== i))}>×</button>
        </div>
      {/each}
      <button onclick={() => (buttons = [...buttons, { text: '', url: '' }])}>+ кнопка-ссылка</button>
    </div>
    <div class="line">
      <label><input type="radio" bind:group={audience} value="all" /> все</label>
      <label><input type="radio" bind:group={audience} value="active" /> активные за</label>
      <input class="n" type="number" min="1" bind:value={days} disabled={audience !== 'active'} /> дн.
      <span class="muted">— получателей: <span class="num">{estimate ?? '…'}</span></span>
    </div>
    <p class="muted">Заблокировавшие бота и забаненные не получат рассылку.</p>
    <div class="line">
      <label><input type="radio" bind:group={when} value="now" /> сейчас</label>
      <label><input type="radio" bind:group={when} value="at" /> в</label>
      <input type="datetime-local" bind:value={at} disabled={when !== 'at'} /> UTC
      {#if when === 'at'}<span class="muted num">{pair(atDate, tz)}</span>{/if}
    </div>
    <div class="line">
      <button disabled={busy || (!text.trim() && !photo)} onclick={sendMe}>Отправить мне</button>
      <span class="grow"></span>
      <HoldButton danger={false} disabled={busy || (!text.trim() && !photo) || text.length > max || estimate === 0} onconfirm={launch}>
        {when === 'now' ? 'Запустить' : 'Запланировать'} ({estimate ?? '…'})
      </HoldButton>
    </div>
    {#if error}<p class="danger">{error}</p>{/if}
    {#if info}<p class="ok">{info}</p>{/if}
  </Box>

  <Box title="Предпросмотр">
    <div class="msg">
      {#if photoURL}<img src={photoURL} alt="" />{/if}
      <div class="text" bind:this={preview}></div>
      {#each buttons.filter((b) => b.text.trim()) as b}<div class="kb">{b.text}</div>{/each}
    </div>
  </Box>
</div>

<Box title="История">
  <table>
    <thead>
      <tr><th>Создана</th><th>Текст</th><th>Аудитория</th><th>Состояние</th><th>Прогресс</th><th>Доставлено</th><th>Заблокировали</th><th>Ошибки</th><th></th></tr>
    </thead>
    <tbody>
      {#each history as b (b.id)}
        <tr>
          <td class="num muted">{utc(b.created_at)}</td>
          <td class="clip" title={b.text}>{b.kind === 'maintenance_warning' ? '⚠ ' : ''}{b.photo_name ? '🖼 ' : ''}{b.text}</td>
          <td>{b.audience === 'active' ? `активные ${b.active_days} дн.` : 'все'}</td>
          <td>
            {states[b.state] ?? b.state}
            {#if b.state === 'scheduled'}<span class="muted num">{utc(b.starts_at)}</span>{/if}
          </td>
          <td><div class="bar"><div style:width="{pct(b)}%"></div></div><span class="num muted">{done(b)}/{b.total}</span></td>
          <td class="num">{b.sent}</td>
          <td class="num">{b.blocked}</td>
          <td class="num" title={Object.entries(b.errors).map(([k, v]) => `${v} × ${k}`).join('\n')}>{b.failed}</td>
          <td class="acts">
            {#if b.state === 'running' || b.state === 'scheduled'}<button onclick={() => control(b, 'pause')}>пауза</button>{/if}
            {#if b.state === 'paused'}<button onclick={() => control(b, 'resume')}>продолжить</button>{/if}
            {#if b.state === 'running' || b.state === 'scheduled' || b.state === 'paused'}
              <HoldButton onconfirm={() => control(b, 'cancel')}>отменить</HoldButton>
            {/if}
          </td>
        </tr>
      {:else}
        <tr><td colspan="9" class="muted">Рассылок ещё не было</td></tr>
      {/each}
    </tbody>
  </table>
</Box>

<style>
  .cols {
    display: grid;
    grid-template-columns: 3fr 2fr;
    gap: var(--s3);
    margin-bottom: var(--s3);
  }
  .cols :global(.box) > :global(*) + :global(*) {
    margin-top: var(--s2);
  }
  textarea {
    width: 100%;
    font-family: var(--font-mono);
  }
  .modes,
  .line {
    display: flex;
    gap: var(--s2);
    align-items: center;
    flex-wrap: wrap;
  }
  .line + .line {
    margin-top: var(--s1);
  }
  .modes .active {
    border-color: var(--accent);
    background: var(--surface-2);
  }
  .grow {
    flex: 1;
  }
  .n {
    width: 5em;
  }
  .x {
    border: none;
    background: none;
  }
  .msg {
    max-width: 360px;
    background: var(--surface-2);
    border: var(--line);
    padding: var(--s2);
  }
  .msg img {
    width: 100%;
    display: block;
    margin-bottom: var(--s2);
  }
  .text {
    word-wrap: break-word;
    min-height: 1.5em;
  }
  .text :global(.spoiler) {
    background: var(--muted);
    color: transparent;
  }
  .text :global(blockquote) {
    border-left: 2px solid var(--accent);
    margin: 0;
    padding-left: var(--s2);
  }
  .text :global(a) {
    color: var(--accent-text);
  }
  .kb {
    margin-top: var(--s1);
    border: var(--line);
    text-align: center;
    padding: var(--s1);
    color: var(--accent-text);
  }
  table {
    width: 100%;
  }
  th,
  td {
    text-align: left;
    padding: var(--s1) var(--s2);
    border-bottom: var(--line);
  }
  th {
    color: var(--muted);
    font-weight: 400;
    font-size: var(--fs-s);
  }
  .clip {
    max-width: 240px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bar {
    width: 100px;
    height: 6px;
    background: var(--surface-2);
    display: inline-block;
    margin-right: var(--s2);
  }
  .bar div {
    height: 100%;
    background: var(--accent);
  }
  .acts {
    display: flex;
    gap: var(--s1);
  }
  .ok {
    color: var(--accent-text);
  }

  /* Phones: one column, rows of controls wrap. */
  @media (max-width: 700px) {
    .cols {
      grid-template-columns: 1fr;
    }
  }
</style>
