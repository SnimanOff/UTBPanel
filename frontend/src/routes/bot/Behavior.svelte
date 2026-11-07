<script lang="ts">
  // How the panel answers users before the bot sees them: antiflood, closed
  // mode with its list, texts, the zone of times in texts and the updates
  // mode (webhook or getUpdates).
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import { defaultTZ, pair, validZone, zones } from '../../lib/tz'
  import AlertRules from '../../lib/ui/AlertRules.svelte'
  import Box from '../../lib/ui/Box.svelte'

  let { botId }: { botId: number } = $props()
  type Flood = {
    enabled: boolean
    limit: number
    window: string
    warn: boolean
    warn_text: string
    autoban: boolean
    strikes: number
    ban_hours: number
  }
  const floodDefault: Flood = {
    enabled: false, limit: 20, window: '10s', warn: true, warn_text: '', autoban: false, strikes: 3, ban_hours: 24,
  }
  let flood = $state<Flood>({ ...floodDefault })
  let closed = $state<Schemas['ClosedState']>({ enabled: false, text: '', allow: [] })
  let unavailable = $state('')
  type Pat = { name: string; pattern: string; regexp?: boolean }
  let patterns = $state<Pat[]>([])
  let tz = $state(defaultTZ)
  let crashUpdates = $state(false)
  let webhook = $state(false)
  let who = $state('')
  let note = $state('')
  let error = $state('')
  let saved = $state('')

  async function load() {
    try {
      const s = await api.get<Record<string, any>>(`/api/bots/${botId}/settings`)
      flood = { ...floodDefault, ...(s.antiflood ?? {}) }
      unavailable = s.texts?.unavailable ?? ''
      patterns = s.log_patterns ?? []
      tz = s.tz || defaultTZ
      crashUpdates = !!s.crash_updates
      webhook = s.updates === 'polling'
      closed = await api.get<Schemas['ClosedState']>(`/api/bots/${botId}/closed`)
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void botId
    load()
  })

  async function run(fn: () => Promise<unknown>, msg: string) {
    error = ''
    try {
      await fn()
      saved = msg
      setTimeout(() => (saved = ''), 3000)
    } catch (e) {
      error = errorText(e)
    }
  }
  const section = (key: string, value: object, msg: string) =>
    run(() => api.put(`/api/bots/${botId}/settings/${key}`, value), msg)

  const saveFlood = () => section('antiflood', flood, 'Антифлуд сохранён')
  const saveTexts = () => section('texts', { unavailable }, 'Тексты сохранены')
  const saveTZ = () => (validZone(tz) ? section('tz', new Blob([JSON.stringify(tz)], { type: 'application/json' }), 'Часовой пояс сохранён') : (error = `Неизвестный пояс ${tz}`))
  const saveUpdates = () =>
    section('updates', new Blob([JSON.stringify(webhook ? 'polling' : 'webhook')], { type: 'application/json' }), webhook ? 'Вебхук для getUpdates включён' : 'Вебхук для getUpdates выключен')
  const saveClosed = () =>
    run(() => api.put(`/api/bots/${botId}/closed`, { enabled: closed.enabled, text: closed.text }), 'Закрытый режим сохранён')

  async function allow(e: SubmitEvent) {
    e.preventDefault()
    await run(() => api.post(`/api/bots/${botId}/closed/allow`, { user: who.trim(), note }), 'Добавлен в список')
    who = note = ''
    load()
  }
  async function remove(uid: number) {
    await run(() => api.del(`/api/bots/${botId}/closed/allow/${uid}`), 'Убран из списка')
    load()
  }
</script>

<div class="cols">
  <Box title="Антифлуд">
    <label class="check"><input type="checkbox" bind:checked={flood.enabled} /> включён</label>
    <div class="line">
      не больше <input class="n" type="number" min="1" bind:value={flood.limit} /> апдейтов за
      <input class="n" bind:value={flood.window} placeholder="10s" />
    </div>
    <label class="check"><input type="checkbox" bind:checked={flood.warn} /> предупреждать</label>
    {#if flood.warn}<input bind:value={flood.warn_text} placeholder="Слишком часто, подождите немного" />{/if}
    <label class="check"><input type="checkbox" bind:checked={flood.autoban} /> автобан</label>
    {#if flood.autoban}
      <div class="line">
        после <input class="n" type="number" min="1" bind:value={flood.strikes} /> нарушений за час — на
        <input class="n" type="number" min="1" bind:value={flood.ban_hours} /> ч
      </div>
    {/if}
    <div><button onclick={saveFlood}>Сохранить</button></div>
  </Box>

  <Box title="Закрытый режим">
    <label class="check"><input type="checkbox" bind:checked={closed.enabled} /> только для списка</label>
    <input bind:value={closed.text} placeholder="Бот пока доступен не всем" />
    <div><button onclick={saveClosed}>Сохранить</button></div>
    <form onsubmit={allow} class="line">
      <input bind:value={who} placeholder="id, @username или me" />
      <input bind:value={note} placeholder="заметка" />
      <button disabled={!who.trim()}>Добавить</button>
    </form>
    <ul>
      {#each closed.allow as a (a.user_id)}
        <li>
          <span class="num">{a.user_id}</span>
          {#if a.username}<span>@{a.username}</span>{/if}
          <span class="muted">{a.note ?? ''}</span>
          <button class="x" title="Убрать" onclick={() => remove(a.user_id)}>×</button>
        </li>
      {:else}
        <li class="muted">Список пуст</li>
      {/each}
    </ul>
  </Box>

  <Box title="Тексты">
    <label>
      Бот недоступен (ядро перезапускается)
      <textarea rows="2" bind:value={unavailable} placeholder="Бот перезапускается, попробуйте через минуту"></textarea>
    </label>
    <div><button onclick={saveTexts}>Сохранить</button></div>
  </Box>

  <AlertRules {botId} />

  <Box title="Шаблоны логов">
    {#each patterns as p, i}
      <div class="line">
        <input class="n2" bind:value={p.name} placeholder="имя" />
        <input class="grow" bind:value={p.pattern} placeholder="подстрока или regexp" />
        <label class="check"><input type="checkbox" bind:checked={p.regexp} /> regexp</label>
        <button class="x" onclick={() => (patterns = patterns.filter((_, j) => j !== i))}>×</button>
      </div>
    {/each}
    <div class="line">
      <button onclick={() => (patterns = [...patterns, { name: '', pattern: '' }])}>+ шаблон</button>
      <button onclick={() => section('log_patterns', patterns, 'Шаблоны сохранены')}>Сохранить</button>
    </div>
    <p class="muted">Совпадения считаются по минутам — счётчики видны во вкладке «Логи» и в порогах. Сообщения о лимитах (too many open files и т. п.) отслеживаются всегда.</p>
  </Box>

  <Box title="Апдейты при падениях">
    <label class="check">
      <input
        type="checkbox"
        bind:checked={crashUpdates}
        onchange={() => section('crash_updates', new Blob([JSON.stringify(crashUpdates)], { type: 'application/json' }), crashUpdates ? 'Сохранение включено' : 'Сохранение выключено, сохранённое удалено')}
      />
      сохранять апдейты, бывшие в обработке при падении или ответе 5xx
    </label>
    <p class="muted">Это сообщения пользователей: последние 20, 7 дней, только во вкладке «Паники». В архив переноса не попадают.</p>
  </Box>

  <Box title="Приём апдейтов">
    <label class="check switch">
      <input type="checkbox" role="switch" bind:checked={webhook} onchange={saveUpdates} />
      <span class="track" aria-hidden="true"></span>
      Включить вебхук
    </label>
    {#if webhook}
      <p class="muted">Бот под polling работает без переделки: ядро отдаёт ему апдейты с вебхука панели в ответ на его getUpdates. Боту под вебхук это не мешает: пока он не вызвал getUpdates, апдейты идут на его PORT как обычно.</p>
    {:else}
      <p class="muted">Апдейты идут только на PORT бота, getUpdates отклоняется. Включите для бота, написанного под polling.</p>
    {/if}
  </Box>

  <Box title="Часовой пояс текстов">
    <input list="zones" bind:value={tz} />
    <datalist id="zones">{#each zones as z}<option value={z}></option>{/each}</datalist>
    {#if validZone(tz)}<p class="muted num">сейчас {pair(new Date(), tz)}</p>{/if}
    <div><button onclick={saveTZ}>Сохранить</button></div>
  </Box>
</div>
{#if error}<p class="danger">{error}</p>{/if}
{#if saved}<p class="ok">{saved}</p>{/if}

<style>
  .cols {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--s3);
    margin-bottom: var(--s3);
  }
  .cols :global(.box) > :global(*) + :global(*) {
    margin-top: var(--s2);
  }
  label {
    display: flex;
    flex-direction: column;
    gap: var(--s1);
  }
  .check {
    flex-direction: row;
    align-items: center;
  }
  /* A checkbox drawn as a switch. */
  .switch {
    gap: var(--s2);
    cursor: pointer;
  }
  .switch input {
    position: absolute;
    opacity: 0;
    width: 1px;
    height: 1px;
  }
  .track {
    position: relative;
    flex: none;
    width: 34px;
    height: 18px;
    border: var(--line);
    border-radius: var(--radius);
    background: var(--surface-2);
    transition: background 0.15s;
  }
  .track::after {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 12px;
    height: 12px;
    border-radius: var(--radius);
    background: var(--muted);
    transition: transform 0.15s;
  }
  .switch input:checked + .track {
    background: var(--accent);
    border-color: var(--accent-hi);
  }
  .switch input:checked + .track::after {
    background: var(--text);
    transform: translateX(16px);
  }
  .switch input:focus-visible + .track {
    outline: 1px solid var(--accent-hi);
    outline-offset: 2px;
  }
  .line {
    display: flex;
    gap: var(--s2);
    align-items: center;
    flex-wrap: wrap;
  }
  .n {
    width: 5em;
  }
  .n2 {
    width: 8em;
  }
  .grow {
    flex: 1;
  }
  ul {
    list-style: none;
    padding: 0;
  }
  li {
    display: flex;
    gap: var(--s2);
    padding: 2px 0;
    border-bottom: var(--line);
  }
  .x {
    margin-left: auto;
    border: none;
    background: none;
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
