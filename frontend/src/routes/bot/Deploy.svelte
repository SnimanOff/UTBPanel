<script lang="ts">
  // Deploy: stored versions with their build data, upload, switch and
  // rollback by holding (a database backup is made before each), GitHub
  // Releases with autodeploy, and the deploy history.
  import { api, errorText, uploadWithProgress, type Release, type Schemas } from '../../lib/api/client'
  import { store } from '../../lib/api/stores.svelte'
  import { bytes, utc } from '../../lib/format'
  import Box from '../../lib/ui/Box.svelte'
  import HoldButton from '../../lib/ui/HoldButton.svelte'
  import Canary from './Canary.svelte'

  let { botId }: { botId: number } = $props()
  type GH = {
    repo: string
    asset_mask: string
    auto_deploy: boolean
    has_token: boolean
    hook_url: string
    last_tag?: string
    checked_at?: string
    last_error?: string
  }
  let releases = $state<Release[]>([])
  let deploys = $state<Schemas['DeployRecord'][]>([])
  let reports = $state<Schemas['DeployReport'][]>([])
  let gh = $state<GH | null>(null)
  let repo = $state('')
  let mask = $state('*linux_amd64*')
  let auto = $state(false)
  let token = $state('')
  let hookSecret = $state('')
  let file = $state<HTMLInputElement>()
  let version = $state('')
  let busy = $state(false)
  let error = $state('')
  let info = $state('')

  async function load() {
    try {
      ;[releases, deploys] = await Promise.all([
        api.get<Release[]>(`/api/bots/${botId}/releases`),
        api.get<Schemas['DeployRecord'][]>(`/api/bots/${botId}/deploys`),
      ])
      reports = await api.get<Schemas['DeployReport'][]>(`/api/bots/${botId}/deploy-reports`)
      const g = await api.get<{ github: GH | null }>(`/api/bots/${botId}/github`)
      gh = g.github
      if (gh) {
        repo = gh.repo
        mask = gh.asset_mask
        auto = gh.auto_deploy
      }
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void botId
    load()
  })

  async function run(fn: () => Promise<unknown>, msg: string) {
    busy = true
    error = ''
    info = ''
    try {
      await fn()
      info = msg
      setTimeout(() => (info = ''), 4000)
      store.refresh(botId)
      await load()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }

  // A version with tgpanel.yaml shows its diff first; the deploy then sends
  // the settings revision the owner saw, so a changed diff is not applied.
  type ReleaseConfig = { version: string; yaml: string; diff: Schemas['BotConfigDiff']; settings_version: string; error?: string }
  let pending = $state<ReleaseConfig | null>(null)
  $effect(() => {
    void botId
    pending = null
  })

  const doSwitch = (v: string, configVersion?: string) =>
    run(async () => {
      await api.post(`/api/bots/${botId}/releases/${encodeURIComponent(v)}/switch`, configVersion ? { config_version: configVersion } : undefined)
      pending = null
    }, `Переключено на ${v}`)

  async function switchTo(r: Release) {
    if (!r.config) return doSwitch(r.version)
    error = ''
    try {
      const c = await api.get<Omit<ReleaseConfig, 'version'>>(`/api/bots/${botId}/releases/${encodeURIComponent(r.version)}/config`)
      if (!c.error && !c.diff.changes.length) return doSwitch(r.version)
      pending = { ...c, version: r.version }
    } catch (e) {
      error = errorText(e)
    }
  }

  // Upload with progress: a large binary goes through gate for minutes, and
  // leaving the page cancels it, so the form shows how much is sent and
  // asks before closing the tab.
  type Sending = { loaded: number; total: number; started: number; abort: AbortController }
  let sending = $state<Sending | null>(null)
  let uploadError = $state('')
  let uploaded = $state('')

  const pctSent = (s: Sending) => (s.total ? Math.min(100, (s.loaded / s.total) * 100) : 0)
  const speed = (s: Sending) => {
    const sec = (performance.now() - s.started) / 1000
    return sec > 0.5 ? s.loaded / sec : 0
  }
  function eta(s: Sending): string {
    const v = speed(s)
    if (!v) return ''
    const left = Math.round((s.total - s.loaded) / v)
    return left >= 60 ? `~${Math.floor(left / 60)} мин ${left % 60} с` : `~${left} с`
  }

  $effect(() => {
    if (!sending) return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  })

  async function upload(e: SubmitEvent) {
    e.preventDefault()
    const f = file?.files?.[0]
    if (!f || sending) return
    const fd = new FormData()
    if (version.trim()) fd.append('version', version.trim())
    fd.append('file', f)
    uploadError = ''
    uploaded = ''
    const s: Sending = { loaded: 0, total: f.size, started: performance.now(), abort: new AbortController() }
    sending = s
    try {
      const r = await uploadWithProgress<Release>(
        `/api/bots/${botId}/releases`,
        fd,
        (p) => {
          if (sending) {
            sending.loaded = p.loaded
            sending.total = p.total
          }
        },
        s.abort.signal,
      )
      version = ''
      if (file) file.value = ''
      uploaded = `Загружена версия ${r.version} (${bytes(r.size)})`
      store.refresh(botId)
      await load()
    } catch (err) {
      uploadError = errorText(err)
    } finally {
      sending = null
    }
  }

  function saveGitHub(e: SubmitEvent) {
    e.preventDefault()
    run(async () => {
      const body: Record<string, unknown> = { repo, asset_mask: mask, auto_deploy: auto }
      if (token) body.token = token
      const r = await api.put<{ hook_secret?: string }>(`/api/bots/${botId}/github`, body)
      token = ''
      if (r.hook_secret) hookSecret = r.hook_secret
    }, repo ? 'GitHub сохранён' : 'GitHub отключён')
  }
  const dropToken = () => run(() => api.put(`/api/bots/${botId}/github`, { repo, asset_mask: mask, auto_deploy: auto, token: '' }), 'Токен удалён')
  const check = () => run(() => api.post(`/api/bots/${botId}/github/check`), 'Проверено')

  type Sum = Schemas['DeploySummary']
  const rows: [string, (s: Sum) => string][] = [
    ['апдейты', (s) => String(s.updates)],
    ['ошибки', (s) => `${s.errors} (${(s.err_rate * 100).toFixed(1)}%)`],
    ['p95', (s) => `${Math.round(s.p95_ms)} мс`],
    ['падения', (s) => String(s.crashes)],
    ['CPU', (s) => `${s.cpu_avg.toFixed(1)}%`],
    ['RAM', (s) => bytes(s.rss_max)],
  ]
  const results: Record<string, string> = {
    running: 'идёт', observing: 'наблюдение', ok: 'готово', failed: 'ошибка', rolled_back: 'откачен',
  }
</script>

{#if error}<p class="danger pre">{error}</p>{/if}
{#if info}<p class="ok">{info}</p>{/if}

<Box title="Версии">
  <table>
    <thead><tr><th>Версия</th><th>Источник</th><th>Загружена</th><th>Сборка</th><th>Размер</th><th></th></tr></thead>
    <tbody>
      {#each releases as r (r.version)}
        <tr class:cur={r.current}>
          <td class="num">
            {r.version}
            {#if r.current}<span class="tag">текущая</span>{/if}
            {#if r.previous}<span class="tag muted">предыдущая</span>{/if}
            {#if r.config}<span class="tag muted" title="в релизе есть tgpanel.yaml">настройки</span>{/if}
          </td>
          <td>{r.source}</td>
          <td class="num muted">{utc(r.uploaded)}</td>
          <td class="muted num" title={r.build.module}>
            {r.build.go_version ?? ''}
            {#if r.build.revision}· {r.build.revision.slice(0, 8)}{r.build.modified ? '+' : ''}{/if}
            {#if r.build.vcs_time}· {utc(r.build.vcs_time)}{/if}
          </td>
          <td class="num muted">{bytes(r.size)}</td>
          <td>
            {#if !r.current}
              <HoldButton danger={r.previous} disabled={busy} onconfirm={() => switchTo(r)}>
                {r.previous ? '↶ Откатить' : 'Переключить'}
              </HoldButton>
            {/if}
          </td>
        </tr>
      {:else}
        <tr><td colspan="6" class="muted">Версий нет — загрузите бинарник или подключите GitHub.</td></tr>
      {/each}
    </tbody>
  </table>
  {#if pending}
    <div class="relcfg">
      <h4>Настройки из tgpanel.yaml версии <span class="num">{pending.version}</span></h4>
      {#if pending.error}
        <p class="danger pre">{pending.error}</p>
        <p class="muted">С этим файлом версию не развернуть: исправьте его в репозитории и выпустите новый релиз.</p>
      {:else}
        {#each pending.diff.changes as change}
          <div class="change">
            <strong class="mono">{change.path}</strong>
            <span class:danger={change.level === 'confirm'} class:muted={change.level !== 'confirm'}>{change.level === 'confirm' ? 'С подтверждением' : 'Сразу'}</span>
            <del class="mono">{JSON.stringify(change.before)}</del>
            <ins class="mono">{JSON.stringify(change.after)}</ins>
          </div>
        {/each}
        {#if pending.diff.restart}<p class="muted">Переменные и секреты вступят в силу при запуске новой версии.</p>{/if}
      {/if}
      <div class="line">
        {#if !pending.error}
          <HoldButton disabled={busy} onconfirm={() => {
            if (pending) doSwitch(pending.version, pending.settings_version)
          }}>
            Применить и переключить
          </HoldButton>
        {/if}
        <button disabled={busy} onclick={() => (pending = null)}>Отмена</button>
      </div>
    </div>
  {/if}
  <p class="muted">Хранятся 5 последних версий. Перед каждым переключением делается бэкап базы бота.</p>
  <form class="line" onsubmit={upload}>
    <input type="file" bind:this={file} disabled={!!sending} />
    <input bind:value={version} placeholder="версия (необязательно)" disabled={!!sending} />
    <button disabled={busy || !!sending}>Загрузить</button>
  </form>
  {#if sending}
    {@const done = sending.loaded >= sending.total}
    <div class="upload" aria-live="polite">
      <div class="track" class:wait={done} role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(pctSent(sending))}>
        <div style:width="{pctSent(sending)}%"></div>
      </div>
      <div class="line meta">
        {#if done}
          <span>Файл отправлен, сервер проверяет и сохраняет бинарник…</span>
        {:else}
          <span class="num">{Math.floor(pctSent(sending))}%</span>
          <span class="num muted">{bytes(sending.loaded)} из {bytes(sending.total)}</span>
          {#if speed(sending)}<span class="num muted">{bytes(speed(sending))}/с · осталось {eta(sending)}</span>{/if}
          <button type="button" onclick={() => sending?.abort.abort()}>Отменить</button>
        {/if}
      </div>
      <p class="muted small">Не закрывайте и не обновляйте страницу, пока идёт загрузка: это её прервёт.</p>
    </div>
  {/if}
  {#if uploadError}<p class="danger pre">{uploadError}</p>{/if}
  {#if uploaded}<p class="ok">{uploaded}</p>{/if}
</Box>

<div class="cols">
  <Canary {botId} {releases} onchange={load} />
  <Box title="GitHub Releases">
    <form onsubmit={saveGitHub}>
      <label>Репозиторий <input bind:value={repo} placeholder="owner/repo" /></label>
      <label>Файл релиза <input bind:value={mask} placeholder="*linux_amd64*" /></label>
      <label>
        Токен для приватного репозитория {#if gh?.has_token}<span class="muted">— сохранён</span>{/if}
        <input type="password" bind:value={token} placeholder={gh?.has_token ? 'заменить' : 'ghp_…'} autocomplete="off" />
      </label>
      <label class="check"><input type="checkbox" bind:checked={auto} /> автодеплой нового релиза</label>
      <div class="line">
        <button class="primary" disabled={busy}>Сохранить</button>
        {#if gh}<button type="button" disabled={busy} onclick={check}>Проверить сейчас</button>{/if}
        {#if gh?.has_token}<button type="button" disabled={busy} onclick={dropToken}>Удалить токен</button>{/if}
      </div>
    </form>
    {#if gh}
      <p class="muted">
        Опрос раз в 5 минут{#if gh.checked_at}, последний — <span class="num">{utc(gh.checked_at)}</span>{/if}{#if gh.last_tag}, последний релиз <span class="num">{gh.last_tag}</span>{/if}.
      </p>
      {#if gh.last_error}<p class="danger">{gh.last_error}</p>{/if}
      <p class="muted">Вебхук (необязательно, быстрее опроса): Settings → Webhooks, событие Releases, тип application/json</p>
      <code class="sel">{gh.hook_url}</code>
      {#if hookSecret}
        <p>Секрет вебхука — показывается один раз:</p>
        <code class="sel">{hookSecret}</code>
      {/if}
    {/if}
  </Box>

  <Box title="История деплоев">
    <table>
      <thead><tr><th>Когда</th><th>Версия</th><th>Кто</th><th>Итог</th></tr></thead>
      <tbody>
        {#each deploys as d (d.id)}
          <tr>
            <td class="num muted">{utc(d.started_at)}</td>
            <td class="num">{d.from || '—'} → {d.to}</td>
            <td class="muted">{d.actor}</td>
            <td class:danger={d.result === 'failed' || d.result === 'rolled_back'} title={d.error ?? ''}>{results[d.result] ?? d.result}</td>
          </tr>
        {:else}
          <tr><td colspan="4" class="muted">Деплоев ещё не было</td></tr>
        {/each}
      </tbody>
    </table>
  </Box>
</div>

{#if reports.length}
  <Box title="Отчёты: час до и час после деплоя">
    <div class="reports">
      {#each reports as r (r.deploy_id)}
        <div class="report" class:bad={r.worse.length > 0}>
          <div class="num">{r.version} · {utc(new Date(r.ts * 1000).toISOString())}</div>
          <table>
            <thead><tr><th></th><th>до</th><th>после</th></tr></thead>
            <tbody>
              {#each rows as [label, f]}<tr><td class="muted">{label}</td><td class="num">{f(r.before)}</td><td class="num">{f(r.after)}</td></tr>{/each}
            </tbody>
          </table>
          {#each r.worse as w}<p class="danger">{w}</p>{/each}
        </div>
      {/each}
    </div>
  </Box>
{/if}

<style>
  .relcfg {
    border: var(--line);
    padding: var(--s2) var(--s3);
    margin: var(--s3) 0;
  }
  .relcfg h4 {
    margin: 0 0 var(--s2);
  }
  .change {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: var(--s1) var(--s3);
    border-bottom: var(--line);
    padding: var(--s2) 0;
  }
  del,
  ins {
    grid-column: 1 / -1;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    text-decoration: none;
  }
  del {
    color: var(--danger-text);
  }
  ins {
    color: var(--accent-text);
  }
  .reports {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(260px, 100%), 1fr));
    gap: var(--s3);
  }
  .report {
    border: var(--line);
    padding: var(--s2);
  }
  .report.bad {
    border-color: var(--danger);
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
  .cur td {
    background: var(--surface-2);
  }
  .tag {
    font-size: var(--fs-xs);
    border: var(--line);
    padding: 0 var(--s1);
    margin-left: var(--s1);
  }
  .cols {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--s3);
    margin: var(--s3) 0;
  }
  form,
  label {
    display: flex;
    flex-direction: column;
    gap: var(--s1);
  }
  form {
    gap: var(--s2);
  }
  .check {
    flex-direction: row;
    align-items: center;
  }
  .line {
    display: flex;
    flex-direction: row;
    gap: var(--s2);
    align-items: center;
    margin-top: var(--s2);
  }
  .sel {
    user-select: all;
    display: block;
    word-break: break-all;
    margin: var(--s1) 0;
  }
  .ok {
    color: var(--accent-text);
  }
  .upload {
    margin-top: var(--s2);
  }
  .track {
    height: 8px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .track div {
    height: 100%;
    background: var(--accent);
    transition: width 0.2s linear;
  }
  /* Bytes are sent; the server's answer is pending. */
  .track.wait div {
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.45;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .track div {
      transition: none;
    }
    .track.wait div {
      animation: none;
    }
  }
  .meta {
    margin-top: var(--s1);
    flex-wrap: wrap;
  }
  .small {
    font-size: var(--fs-s);
  }
  .pre {
    white-space: pre-wrap;
  }

  /* Phones: one column, rows of controls wrap. */
  @media (max-width: 700px) {
    .cols {
      grid-template-columns: 1fr;
    }
    .line {
      flex-wrap: wrap;
    }
  }
</style>
