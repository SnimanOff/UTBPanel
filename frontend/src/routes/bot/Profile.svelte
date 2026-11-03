<script lang="ts">
  // Telegram profile: name, descriptions and commands per language ("по
  // умолчанию" and chosen ones), the menu button. "Сохранить" sends only
  // changed fields; Telegram's refusals are shown at the field.
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import { utc } from '../../lib/format'
  import Box from '../../lib/ui/Box.svelte'

  let { botId }: { botId: number } = $props()
  type View = Schemas['ProfileView']
  type Cmd = Schemas['BotCommand']
  type Errors = Schemas['ProfileSaved']['errors']

  let langs = $state<string[]>([''])
  let lang = $state('')
  let newLang = $state('')
  let orig = $state<View>()
  let name = $state('')
  let description = $state('')
  let short = $state('')
  let commands = $state<Cmd[]>([])
  let menuType = $state<'commands' | 'web_app' | 'default'>('default')
  let menuText = $state('')
  let menuURL = $state('')
  let errors = $state<Errors>({})
  let error = $state('')
  let saved = $state('')
  let busy = $state(false)
  let drag = $state<number | null>(null)

  async function load() {
    error = ''
    errors = {}
    busy = true
    try {
      const v = await api.get<View>(`/api/bots/${botId}/profile?lang=${encodeURIComponent(lang)}`)
      orig = v
      name = v.profile.name
      description = v.profile.description
      short = v.profile.short_description
      commands = v.profile.commands.map((c) => ({ ...c }))
      menuType = (v.menu.type as typeof menuType) || 'default'
      menuText = v.menu.text ?? ''
      menuURL = v.menu.web_app?.url ?? ''
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
  $effect(() => {
    void [botId, lang]
    load()
  })

  const sameCmds = (a: Cmd[], b: Cmd[]) =>
    a.length === b.length && a.every((c, i) => c.command === b[i].command && c.description === b[i].description)
  const menu = $derived<Schemas['MenuButton']>(
    menuType === 'web_app' ? { type: 'web_app', text: menuText, web_app: { url: menuURL } } : { type: menuType },
  )
  const changes = $derived.by(() => {
    if (!orig) return {}
    const p = orig.profile
    const c: Record<string, unknown> = {}
    if (name !== p.name) c.name = name
    if (description !== p.description) c.description = description
    if (short !== p.short_description) c.short_description = short
    const cleaned = commands.filter((x) => x.command.trim())
    if (!sameCmds(cleaned, p.commands)) c.commands = cleaned
    if (JSON.stringify(menu) !== JSON.stringify(orig.menu.type === 'web_app' ? orig.menu : { type: orig.menu.type })) c.menu = menu
    return c
  })
  const dirty = $derived(Object.keys(changes).length > 0)

  async function save() {
    busy = true
    error = ''
    try {
      const r = await api.patch<Schemas['ProfileSaved']>(`/api/bots/${botId}/profile`, { lang, ...changes })
      errors = r.errors
      const failed = Object.keys(r.errors)
      saved = failed.length ? `Сохранено, кроме: ${failed.join(', ')}` : 'Сохранено'
      setTimeout(() => (saved = ''), 4000)
      const keep = { ...errors }
      await load()
      errors = keep
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }

  function addLang(e: SubmitEvent) {
    e.preventDefault()
    const l = newLang.trim().toLowerCase()
    if (/^[a-z]{2}$/.test(l) && !langs.includes(l)) langs = [...langs, l]
    if (/^[a-z]{2}$/.test(l)) lang = l
    newLang = ''
  }
  function drop(i: number) {
    if (drag === null || drag === i) return
    const c = [...commands]
    const [m] = c.splice(drag, 1)
    c.splice(i, 0, m)
    commands = c
    drag = null
  }
</script>

{#snippet fieldError(key: string)}
  {#if errors[key]}
    <span class="danger">
      Telegram: {errors[key].message}{#if errors[key].retry_at}{" "}— можно повторить после {utc(errors[key].retry_at)}{/if}
    </span>
  {/if}
{/snippet}

<div class="langs">
  {#each langs as l}
    <button class:active={lang === l} onclick={() => (lang = l)}>{l || 'по умолчанию'}</button>
  {/each}
  <form onsubmit={addLang}>
    <input bind:value={newLang} placeholder="ru, en…" maxlength="2" />
    <button>+ язык</button>
  </form>
  <span class="grow"></span>
  {#if saved}<span class="ok">{saved}</span>{/if}
  <button class="primary" disabled={!dirty || busy} onclick={save}>Сохранить</button>
</div>
{#if error}<p class="danger">{error}</p>{/if}

<div class="cols">
  <Box title="Профиль">
    <label>
      Имя <span class="muted num">{name.length}/64</span>
      <input bind:value={name} maxlength="64" />
      {@render fieldError('name')}
    </label>
    <label>
      Короткое описание <span class="muted num">{short.length}/120</span>
      <textarea rows="2" bind:value={short} maxlength="120"></textarea>
      {@render fieldError('short_description')}
    </label>
    <label>
      Описание (видно в пустом чате) <span class="muted num">{description.length}/512</span>
      <textarea rows="5" bind:value={description} maxlength="512"></textarea>
      {@render fieldError('description')}
    </label>
  </Box>

  <div class="stack">
    <Box title="Команды">
      <ul>
        {#each commands as c, i (i)}
          <li
            draggable="true"
            class:over={drag !== null && drag !== i}
            ondragstart={() => (drag = i)}
            ondragover={(e) => e.preventDefault()}
            ondrop={() => drop(i)}
          >
            <span class="grip" title="Перетащите">⋮⋮</span>
            <span class="muted">/</span><input class="cmd" bind:value={c.command} placeholder="start" maxlength="32" />
            <input bind:value={c.description} placeholder="описание" maxlength="256" />
            <button class="x" title="Удалить" onclick={() => (commands = commands.filter((_, j) => j !== i))}>×</button>
          </li>
        {:else}
          <li class="muted">Команд нет</li>
        {/each}
      </ul>
      <button onclick={() => (commands = [...commands, { command: '', description: '' }])}>+ команда</button>
      {@render fieldError('commands')}
    </Box>

    <Box title="Кнопка меню">
      <select bind:value={menuType}>
        <option value="default">по умолчанию</option>
        <option value="commands">список команд</option>
        <option value="web_app">Web App</option>
      </select>
      {#if menuType === 'web_app'}
        <input bind:value={menuText} placeholder="текст кнопки" />
        <input bind:value={menuURL} placeholder="https://…" />
      {/if}
      <p class="muted">Одна для всех языков.</p>
      {@render fieldError('menu')}
    </Box>
  </div>
</div>

<style>
  .langs {
    display: flex;
    gap: var(--s2);
    align-items: center;
    margin-bottom: var(--s3);
  }
  .langs .active {
    border-color: var(--accent);
    background: var(--surface-2);
  }
  .langs form {
    display: flex;
    gap: var(--s1);
  }
  .langs input {
    width: 5em;
  }
  .grow {
    flex: 1;
  }
  .cols {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--s3);
  }
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--s3);
  }
  label {
    display: flex;
    flex-direction: column;
    gap: var(--s1);
    margin-bottom: var(--s3);
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0 0 var(--s2);
  }
  li {
    display: flex;
    gap: var(--s1);
    align-items: center;
    padding: 2px 0;
  }
  li.over:hover {
    outline: 1px dashed var(--accent);
  }
  .grip {
    cursor: grab;
    color: var(--muted);
  }
  .cmd {
    width: 9em;
  }
  li input:last-of-type {
    flex: 1;
  }
  .x {
    border: none;
    background: none;
  }
  select,
  .stack input {
    width: 100%;
    margin-bottom: var(--s2);
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
