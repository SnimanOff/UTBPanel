<script lang="ts">
  // Update in three steps. 1: a new version — a bot that runs an uploaded
  // binary asks for a new binary, a bot fed by GitHub Releases checks for a
  // new release. 2: a test run on a test bot (its own token and database,
  // optionally with this bot's variables and secrets) to try it in Telegram.
  // 3: the deploy — the current version stops, the new one starts, with a
  // backup before and autorollback after.
  import { api, errorText, uploadWithProgress, type Release, type Schemas } from '../../lib/api/client'
  import { store } from '../../lib/api/stores.svelte'
  import { bytes, uptime, utc } from '../../lib/format'
  import { link } from '../../lib/router.svelte'
  import Box from '../../lib/ui/Box.svelte'
  import HoldButton from '../../lib/ui/HoldButton.svelte'

  type Trial = Schemas['Trial']
  let {
    botId,
    releases,
    github,
    onchange,
  }: { botId: number; releases: Release[]; github: { repo: string } | null; onchange: () => void | Promise<void> } = $props()

  let trial = $state<Trial | null>(null)
  let lastTestBot = $state(0)
  let version = $state('')
  let testBot = $state(0)
  let copyConfig = $state(true)
  let busy = $state(false)
  let error = $state('')
  let info = $state('')

  const current = $derived(releases.find((r) => r.current))
  // Upload unless the bot is fed by GitHub (or its version came from there).
  const fromGitHub = $derived(!!github?.repo && (!current || current.source === 'github'))
  // Versions stored after the current one: the candidates for the update.
  const fresh = $derived(releases.filter((r) => !r.current && (!current || r.uploaded > current.uploaded)))
  const others = $derived(store.bots.filter((b) => b.id !== botId))

  $effect(() => {
    // The newest fresh version by default; keep the choice while it exists.
    if (!fresh.some((r) => r.version === version)) version = fresh[0]?.version ?? ''
  })
  $effect(() => {
    if (testBot && others.some((b) => b.id === testBot)) return
    const guess = others.find((b) => b.id === lastTestBot) ?? others.find((b) => /test|тест/i.test(`${b.name} ${b.username}`))
    testBot = guess?.id ?? 0
  })

  async function loadTrial() {
    try {
      const r = await api.get<Schemas['TrialInfo']>(`/api/bots/${botId}/trial`)
      trial = r.trial ?? null
      lastTestBot = r.last_test_bot ?? 0
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void botId
    trial = null
    loadTrial()
    // The test bot's process changes on its own (crashes, restarts).
    const t = setInterval(() => trial && loadTrial(), 4000)
    return () => clearInterval(t)
  })

  async function run(fn: () => Promise<unknown>, msg = '') {
    busy = true
    error = ''
    info = ''
    try {
      await fn()
      if (msg) {
        info = msg
        setTimeout(() => (info = ''), 6000)
      }
    } catch (e) {
      error = errorText(e)
    } finally {
      store.refresh(botId)
      await loadTrial()
      await onchange()
      busy = false
    }
  }

  // ── 1. New version ────────────────────────────────────────────────────
  type Sending = { loaded: number; total: number; abort: AbortController }
  let sending = $state<Sending | null>(null)
  let file = $state<HTMLInputElement>()
  let name = $state('')

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
    if (name.trim()) fd.append('version', name.trim())
    fd.append('file', f)
    error = ''
    info = ''
    const s: Sending = { loaded: 0, total: f.size, abort: new AbortController() }
    sending = s
    try {
      const r = await uploadWithProgress<Release>(`/api/bots/${botId}/releases`, fd, (p) => {
        if (sending) {
          sending.loaded = p.loaded
          sending.total = p.total
        }
      }, s.abort.signal)
      name = ''
      if (file) file.value = ''
      info = `Загружена версия ${r.version} (${bytes(r.size)})`
      await onchange()
      version = r.version
    } catch (err) {
      error = errorText(err)
    } finally {
      sending = null
    }
  }
  const pct = (s: Sending) => (s.total ? Math.min(100, (s.loaded / s.total) * 100) : 0)

  const checkGitHub = () => run(() => api.post(`/api/bots/${botId}/github/check`), 'GitHub проверен')

  // ── 2. Test run ───────────────────────────────────────────────────────
  const startTrial = (v = version, bot = testBot, copy = copyConfig) =>
    run(async () => {
      const t = await api.post<Trial>(`/api/bots/${botId}/trial`, { version: v, test_bot_id: bot, copy_config: copy })
      if (t.error) throw new Error(`Тестовый бот не запустился: ${t.error}`)
    }, `Версия ${v} запущена на тестовом боте`)
  const stopTrial = () => run(() => api.del(`/api/bots/${botId}/trial`), 'Тест завершён, тестовый бот остановлен')

  function trialState(t: Trial): { text: string; bad: boolean } {
    if (t.error) return { text: `не запустилась: ${t.error}`, bad: true }
    if (t.process.running) {
      const crashes = t.restarts ? `, падала ${t.restarts} раз` : ''
      return { text: `работает ${uptime(t.process.started)}${crashes}`, bad: t.restarts > 0 }
    }
    return { text: t.restarts ? `упала (код ${t.process.last_exit})` : 'остановлена', bad: t.restarts > 0 }
  }

  // ── 3. Deploy ─────────────────────────────────────────────────────────
  // A version with tgpanel.yaml first shows the settings it changes; the
  // deploy then carries the settings revision the owner saw.
  type ReleaseConfig = { yaml: string; diff: Schemas['BotConfigDiff']; settings_version: string; error?: string }
  let pending = $state<{ version: string; viaTrial: boolean; cfg: ReleaseConfig } | null>(null)
  $effect(() => {
    void botId
    pending = null
  })

  function deployNow(v: string, viaTrial: boolean, configVersion?: string) {
    const body = configVersion ? { config_version: configVersion } : undefined
    return run(async () => {
      if (viaTrial) await api.post(`/api/bots/${botId}/trial/promote`, body)
      else await api.post(`/api/bots/${botId}/releases/${encodeURIComponent(v)}/switch`, body)
      pending = null
    }, `Бот обновлён до ${v}. Минуту идёт наблюдение: при падениях или ошибках версия откатится сама.`)
  }

  async function deploy(v: string, viaTrial: boolean) {
    const r = releases.find((x) => x.version === v)
    if (!r?.config) return deployNow(v, viaTrial)
    error = ''
    try {
      const cfg = await api.get<ReleaseConfig>(`/api/bots/${botId}/releases/${encodeURIComponent(v)}/config`)
      if (!cfg.error && !cfg.diff.changes.length) return deployNow(v, viaTrial)
      pending = { version: v, viaTrial, cfg }
    } catch (e) {
      error = errorText(e)
    }
  }
</script>

<Box title="Обновление">
  {#if error}<p class="danger pre">{error}</p>{/if}
  {#if info}<p class="ok">{info}</p>{/if}

  <div class="steps">
    <section>
      <h3><span class="n">1</span> Новая версия</h3>
      {#if current}
        <p class="muted">
          Сейчас работает <span class="num">{current.version}</span> —
          {current.source === 'github' ? 'из GitHub Releases' : 'загружена бинарником'}, {utc(current.uploaded)}.
        </p>
      {:else}
        <p class="muted">У бота ещё нет версии.</p>
      {/if}

      {#if fromGitHub}
        <p>Бот обновляется из <span class="mono">{github?.repo}</span>: проверьте, нет ли нового релиза.</p>
        <div class="line">
          <button disabled={busy} onclick={checkGitHub}>Проверить GitHub</button>
        </div>
        <details>
          <summary class="muted">или загрузить бинарник вручную</summary>
          {@render uploadForm()}
        </details>
      {:else}
        <p>{current ? 'Загрузите новый бинарник' : 'Загрузите бинарник'} (статический linux/amd64).</p>
        {@render uploadForm()}
      {/if}

      {#if fresh.length}
        <label class="pick">
          Версия для обновления
          <select bind:value={version} disabled={busy}>
            {#each fresh as r (r.version)}
              <option value={r.version}>{r.version} · {r.source === 'github' ? 'GitHub' : 'бинарник'} · {utc(r.uploaded)}</option>
            {/each}
          </select>
        </label>
      {:else if current}
        <p class="muted small">Новее текущей версий нет.</p>
      {/if}
    </section>

    <section>
      <h3><span class="n">2</span> Тест на тестовом боте</h3>
      {#if trial}
        {@const st = trialState(trial)}
        <div class="trial" class:bad={st.bad}>
          <div>
            Версия <span class="num">{trial.version}</span> на боте
            <strong>{trial.test_bot}</strong>
            {#if trial.test_username}
              — <a href="https://t.me/{trial.test_username}" target="_blank" rel="noopener">@{trial.test_username}</a>
            {/if}
          </div>
          <div class:danger={st.bad}>{st.text}</div>
          <div class="muted small">
            с {utc(trial.started_at)} · {trial.copied ? 'с переменными и секретами этого бота' : 'со своими настройками'}
            · <a href="/bots/{trial.test_bot_id}/logs" use:link>логи тестового бота</a>
          </div>
          <p class="small">Напишите тестовому боту в Telegram и проверьте, что всё работает.</p>
          <div class="line">
            <button disabled={busy} onclick={() => trial && startTrial(trial.version, trial.test_bot_id, trial.copied)}>Перезапустить тест</button>
            <button disabled={busy} onclick={stopTrial}>Отменить тест</button>
          </div>
          {#if version && version !== trial.version}
            <p class="muted small">Выбрана другая версия ({version}) — запустите тест заново:</p>
            <div class="line"><button disabled={busy} onclick={() => startTrial()}>Тестировать {version}</button></div>
          {/if}
        </div>
      {:else if !others.length}
        <p class="muted">Добавьте в панель второго бота со своим токеном — он будет тестовым.</p>
      {:else}
        <label class="pick">
          Тестовый бот
          <select bind:value={testBot} disabled={busy}>
            <option value={0} disabled>— выберите —</option>
            {#each others as b (b.id)}
              <option value={b.id}>{b.name}{b.username ? ` (@${b.username})` : ''}{b.status.running ? ' · работает' : ''}</option>
            {/each}
          </select>
        </label>
        <label class="check">
          <input type="checkbox" bind:checked={copyConfig} disabled={busy} />
          дать ему переменные и секреты этого бота (кроме токена)
        </label>
        <p class="muted small">
          Тестовый бот остановится, получит копию версии и запустится со своим токеном и своей базой.
          {#if copyConfig}Его переменные и секреты будут заменены.{/if}
        </p>
        <div class="line">
          <button class="primary" disabled={busy || !version || !testBot} onclick={() => startTrial()}>
            Запустить тест{version ? ` ${version}` : ''}
          </button>
        </div>
      {/if}
    </section>

    <section>
      <h3><span class="n">3</span> Обновить бота</h3>
      <p class="muted small">
        Текущая версия остановится и запустится новая. Перед этим делается бэкап базы; первую минуту идёт
        наблюдение, при падении или росте ошибок — автоматический откат.
      </p>
      {#if trial && !trial.error}
        <div class="line">
          <HoldButton danger={false} disabled={busy} onconfirm={() => trial && deploy(trial.version, true)}>
            Всё работает — обновить до {trial.version}
          </HoldButton>
        </div>
      {/if}
      {#if version && (!trial || trial.version !== version || trial.error)}
        <div class="line">
          <HoldButton disabled={busy} onconfirm={() => deploy(version, false)}>Обновить до {version} без теста</HoldButton>
        </div>
      {:else if !trial}
        <p class="muted small">Сначала загрузите новую версию.</p>
      {/if}
    </section>
  </div>

  {#if pending}
    <div class="relcfg">
      <h4>Настройки из tgpanel.yaml версии <span class="num">{pending.version}</span></h4>
      {#if pending.cfg.error}
        <p class="danger pre">{pending.cfg.error}</p>
        <p class="muted">С этим файлом версию не развернуть: исправьте его и загрузите новую версию.</p>
      {:else}
        {#each pending.cfg.diff.changes as change}
          <div class="change">
            <strong class="mono">{change.path}</strong>
            <span class:danger={change.level === 'confirm'} class:muted={change.level !== 'confirm'}>{change.level === 'confirm' ? 'С подтверждением' : 'Сразу'}</span>
            <del class="mono">{JSON.stringify(change.before)}</del>
            <ins class="mono">{JSON.stringify(change.after)}</ins>
          </div>
        {/each}
      {/if}
      <div class="line">
        {#if !pending.cfg.error}
          <HoldButton disabled={busy} onconfirm={() => pending && deployNow(pending.version, pending.viaTrial, pending.cfg.settings_version)}>
            Применить и обновить
          </HoldButton>
        {/if}
        <button disabled={busy} onclick={() => (pending = null)}>Отмена</button>
      </div>
    </div>
  {/if}
</Box>

{#snippet uploadForm()}
  <form class="line" onsubmit={upload}>
    <input type="file" bind:this={file} disabled={!!sending} />
    <input bind:value={name} placeholder="версия (необязательно)" disabled={!!sending} />
    <button class="primary" disabled={busy || !!sending}>Загрузить</button>
  </form>
  {#if sending}
    {@const done = sending.loaded >= sending.total}
    <div class="upload" aria-live="polite">
      <div class="track" class:wait={done} role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(pct(sending))}>
        <div style:width="{pct(sending)}%"></div>
      </div>
      <div class="line meta">
        {#if done}
          <span>Файл отправлен, сервер проверяет бинарник…</span>
        {:else}
          <span class="num">{Math.floor(pct(sending))}%</span>
          <span class="num muted">{bytes(sending.loaded)} из {bytes(sending.total)}</span>
          <button type="button" onclick={() => sending?.abort.abort()}>Отменить</button>
        {/if}
      </div>
      <p class="muted small">Не закрывайте страницу, пока идёт загрузка.</p>
    </div>
  {/if}
{/snippet}

<style>
  .steps {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: var(--s3);
  }
  section {
    border: var(--line);
    padding: var(--s2) var(--s3);
    min-width: 0;
  }
  h3 {
    margin: 0 0 var(--s2);
    font-size: var(--fs-m, 1rem);
    display: flex;
    gap: var(--s2);
    align-items: center;
  }
  .n {
    display: inline-grid;
    place-items: center;
    width: 1.5em;
    height: 1.5em;
    border: var(--line);
    font-size: var(--fs-s);
  }
  .trial {
    display: flex;
    flex-direction: column;
    gap: var(--s1);
    border-left: 3px solid var(--accent);
    padding-left: var(--s2);
  }
  .trial.bad {
    border-left-color: var(--danger);
  }
  .pick {
    display: flex;
    flex-direction: column;
    gap: var(--s1);
    margin-top: var(--s2);
  }
  .check {
    display: flex;
    gap: var(--s1);
    align-items: center;
    margin-top: var(--s2);
  }
  .line {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s2);
    align-items: center;
    margin-top: var(--s2);
  }
  form.line input:not([type='file']) {
    flex: 1 1 10em;
    min-width: 0;
  }
  details {
    margin-top: var(--s2);
  }
  summary {
    cursor: pointer;
  }
  .relcfg {
    border: var(--line);
    padding: var(--s2) var(--s3);
    margin-top: var(--s3);
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
  }
  .small {
    font-size: var(--fs-s);
  }
  .ok {
    color: var(--accent-text);
  }
  .pre {
    white-space: pre-wrap;
  }
  p {
    margin: var(--s1) 0;
  }
  @media (max-width: 900px) {
    .steps {
      grid-template-columns: 1fr;
    }
  }
</style>
