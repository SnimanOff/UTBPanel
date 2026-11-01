<script lang="ts">
  // Settings: token and secrets (write-only), env, variable renames, group,
  // deletion. Changes of secrets and env apply after a restart — the tab
  // says so and offers the restart right away.
  import { api, ApiError, errorText, type BotView, type SecretInfo } from '../../lib/api/client'
  import { store } from '../../lib/api/stores.svelte'
  import { utc } from '../../lib/format'
  import { router } from '../../lib/router.svelte'
  import Box from '../../lib/ui/Box.svelte'
  import HoldButton from '../../lib/ui/HoldButton.svelte'
  import SecretField from '../../lib/ui/SecretField.svelte'
  import Behavior from './Behavior.svelte'
	import Tasks from './Tasks.svelte'
	import Flags from './Flags.svelte'
	import ConfigDocument from './ConfigDocument.svelte'

  let { bot }: { bot: BotView } = $props()
  let secrets = $state<SecretInfo[]>([])
  let env = $state<[string, string][]>([])
  let renames = $state<[string, string][]>([])
  let tokenError = $state('')
  let error = $state('')
  let saved = $state('')
  let restartNeeded = $state(false)
  let configRevision = $state(0)
  let newName = $state('')
  let newValue = $state('')
  let fileName = $state('')
  let fileInput = $state<HTMLInputElement>()
  const standard = ['BOT_TOKEN', 'PORT', 'WEBHOOK_URL', 'WEBHOOK_SECRET', 'DATABASE_URL', 'DATA_DIR']

  async function load() {
    try {
      secrets = await api.get<SecretInfo[]>(`/api/bots/${bot.id}/secrets`)
      const s = await api.get<Record<string, any>>(`/api/bots/${bot.id}/settings`)
      env = Object.entries((s.env ?? {}) as Record<string, string>)
      renames = Object.entries((s.rename ?? {}) as Record<string, string>)
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void bot.id
    load()
  })

  function done(msg: string, restart = true) {
    saved = msg
    error = ''
    if (restart) restartNeeded = true
    setTimeout(() => (saved = ''), 3000)
  }

  async function saveToken(value: string) {
    tokenError = ''
    try {
      await api.put(`/api/bots/${bot.id}/token`, { token: value })
      store.refresh(bot.id)
      done('Токен сохранён')
    } catch (e) {
      tokenError = e instanceof ApiError && e.code === 'telegram' ? 'Telegram: ' + e.message : errorText(e)
      throw e
    }
  }

  async function saveSecret(name: string, value: string) {
    try {
      await api.put(`/api/bots/${bot.id}/secrets/${encodeURIComponent(name)}`, { value })
      done(`${name} сохранён`)
      load()
    } catch (e) {
      error = errorText(e)
    }
  }

  async function addSecret(e: SubmitEvent) {
    e.preventDefault()
    const file = fileInput?.files?.[0]
    try {
      if (file) {
        const fd = new FormData()
        fd.append('file', file)
        await api.put(`/api/bots/${bot.id}/secrets/${encodeURIComponent(newName)}`, fd)
      } else {
        await api.put(`/api/bots/${bot.id}/secrets/${encodeURIComponent(newName)}`, { value: newValue })
      }
      done(`${newName} сохранён`)
      newName = newValue = fileName = ''
      if (fileInput) fileInput.value = ''
      load()
    } catch (err) {
      error = errorText(err)
    }
  }

  async function deleteSecret(name: string) {
    try {
      await api.del(`/api/bots/${bot.id}/secrets/${encodeURIComponent(name)}`)
      done(`${name} удалён`)
      load()
    } catch (e) {
      error = errorText(e)
    }
  }

  async function saveEnv() {
    try {
      const e = Object.fromEntries(env.filter(([k]) => k.trim()).map(([k, v]) => [k.trim(), v]))
      const r = Object.fromEntries(renames.filter(([k, v]) => k.trim() && v.trim()).map(([k, v]) => [k.trim(), v.trim()]))
      await api.patch(`/api/bots/${bot.id}/settings`, { env: e, rename: r })
      done('Переменные сохранены')
    } catch (err) {
      error = errorText(err)
    }
  }

  async function restart() {
    try {
      await api.post(`/api/bots/${bot.id}/restart`)
      restartNeeded = false
      done('Бот перезапущен', false)
      store.refresh(bot.id)
    } catch (e) {
      error = errorText(e)
    }
  }

  async function setGroup(e: Event) {
    const group_id = Number((e.target as HTMLSelectElement).value)
    try {
      await api.put(`/api/bots/${bot.id}/group`, { group_id })
      store.refresh(bot.id)
      done('Группа изменена', false)
    } catch (err) {
      error = errorText(err)
    }
  }

  async function remove() {
    try {
      await api.del(`/api/bots/${bot.id}`)
      await store.load()
      router.go('/')
    } catch (e) {
      error = errorText(e)
    }
  }

  const userSecrets = $derived(secrets.filter((s) => s.name !== 'BOT_TOKEN' && s.name !== 'WEBHOOK_SECRET'))
</script>

{#if restartNeeded}
  <div class="restart">
    Изменения применятся после рестарта бота.
    <button class="primary" onclick={restart}>↻ Перезапустить сейчас</button>
  </div>
{/if}
{#if saved}<p class="ok">{saved}</p>{/if}
{#if error}<p class="danger pre">{error}</p>{/if}

<ConfigDocument botId={bot.id} onapplied={(restart) => { if (restart) restartNeeded = true; configRevision++; load(); store.refresh(bot.id) }} />

{#key configRevision}<Behavior botId={bot.id} />{/key}

{#if bot.sdk?.modules.includes('tasks')}
  <Tasks botId={bot.id} runnable={bot.state === 'running' && !!bot.status?.running} />
{/if}

{#if bot.sdk?.modules.includes('flags')}<Flags botId={bot.id} />{/if}

<div class="grid">
  <Box title="Токен">
    <SecretField name="BOT_TOKEN" isSet onsave={saveToken} error={tokenError} />
    <p class="muted small">При сохранении токен проверяется через getMe. Сам токен больше нигде не показывается: виден только @{bot.username}.</p>
  </Box>

  <Box title="Секреты">
    {#each userSecrets as s (s.name)}
      <div class="secret-row">
        <SecretField name={s.name} isSet onsave={(v) => saveSecret(s.name, v)} />
        <span class="muted small">{s.kind === 'file' ? 'файл · ' : ''}{utc(s.updated_at)}</span>
        <HoldButton onconfirm={() => deleteSecret(s.name)}>удалить</HoldButton>
      </div>
    {:else}
      <p class="muted small">Секретов нет.</p>
    {/each}
    <form class="add" onsubmit={addSecret}>
      <input class="mono" bind:value={newName} placeholder="ИМЯ" pattern="[A-Za-z_][A-Za-z0-9_]*" required />
      {#if !fileName}
        <input type="password" autocomplete="off" bind:value={newValue} placeholder="значение" />
      {:else}
        <span class="muted">{fileName}</span>
      {/if}
      <label class="file" title="Секрет-файл: путь к нему бот получит в переменной с этим именем">
        файл
        <input type="file" bind:this={fileInput} onchange={() => (fileName = fileInput?.files?.[0]?.name ?? '')} hidden />
      </label>
      <button disabled={!newName || (!newValue && !fileName)}>Добавить</button>
    </form>
    <p class="muted small">Значения только на запись: прочитать их нельзя ни здесь, ни через API.</p>
  </Box>

  <Box title="Переменные окружения">
    {#each env as pair, i}
      <div class="kv">
        <input class="mono" bind:value={pair[0]} placeholder="ИМЯ" />
        <input class="mono" bind:value={pair[1]} placeholder="значение" />
        <button onclick={() => env.splice(i, 1)}>×</button>
      </div>
    {/each}
    <button onclick={() => env.push(['', ''])}>+ переменная</button>
    <h3 class="sub">Имена стандартных переменных</h3>
    <p class="muted small">Если код бота читает, например, TELEGRAM_TOKEN вместо BOT_TOKEN.</p>
    {#each renames as pair, i}
      <div class="kv">
        <select bind:value={pair[0]}>
          {#each standard as s}<option>{s}</option>{/each}
        </select>
        <input class="mono" bind:value={pair[1]} placeholder="новое имя" />
        <button onclick={() => renames.splice(i, 1)}>×</button>
      </div>
    {/each}
    <button onclick={() => renames.push(['BOT_TOKEN', ''])}>+ переназначить</button>
    <div class="save"><button class="primary" onclick={saveEnv}>Сохранить переменные</button></div>
  </Box>

  <Box title="Бот">
    <label class="row">
      <span class="muted">Группа</span>
      <select value={bot.group_id ?? 0} onchange={setGroup}>
        <option value={0}>Без группы</option>
        {#each store.groups as g}<option value={g.id}>{g.name}</option>{/each}
      </select>
    </label>
    <div class="row delete">
      <span class="muted small">Удаление: стоп, снятие вебхука, удаление всех данных бота.</span>
      <HoldButton onconfirm={remove}>Удалить бота</HoldButton>
    </div>
  </Box>
</div>

<style>
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(460px, 1fr));
    gap: var(--s3);
  }
  .restart {
    display: flex;
    align-items: center;
    gap: var(--s3);
    border: 1px solid var(--accent);
    padding: var(--s2) var(--s3);
    margin-bottom: var(--s3);
  }
  .ok {
    color: var(--accent-hi);
  }
  .pre {
    white-space: pre-wrap;
  }
  .small {
    font-size: var(--fs-s);
  }
  .secret-row {
    display: grid;
    grid-template-columns: 1fr auto auto;
    gap: var(--s2);
    align-items: center;
    margin-bottom: var(--s2);
  }
  .add,
  .kv {
    display: flex;
    gap: var(--s2);
    margin: var(--s2) 0;
  }
  .add input:first-child,
  .kv > :first-child {
    width: 200px;
  }
  .kv input:nth-child(2) {
    flex: 1;
  }
  .file {
    border: var(--line);
    padding: var(--s1) var(--s3);
    cursor: pointer;
  }
  .sub {
    margin-top: var(--s4);
    font-size: var(--fs-s);
    color: var(--muted);
  }
  .save {
    margin-top: var(--s3);
  }
  .row {
    display: flex;
    gap: var(--s3);
    align-items: center;
    margin-bottom: var(--s3);
  }
  .delete {
    justify-content: space-between;
  }
</style>
