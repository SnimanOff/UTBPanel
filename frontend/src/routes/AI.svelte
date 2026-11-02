<script lang="ts">
  // Neural networks: everything in one place — connecting over MCP, uploading
  // a local build, the docs for the model, MCP modes by group, and what the
  // AI waits for: confirmations and secrets.
  import { api, errorText, type Group } from '../lib/api/client'
  import { store } from '../lib/api/stores.svelte'
  import Box from '../lib/ui/Box.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'
  import PendingActions from '../lib/ui/PendingActions.svelte'
  import SecretRequests from '../lib/ui/SecretRequests.svelte'

  type Mode = 'off' | 'read' | 'full'
  let groups = $state<Group[]>([])
  let draft = $state<Record<number, Mode>>({})
  let owner = $state(false), loaded = $state(false), saving = $state(0)
  let error = $state(''), message = $state('')
  let botId = $state(0)
  const endpoint = 'http://127.0.0.1:47100'
  const token = '<токен из меню gate>'
  const readme = 'https://github.com/SnimanOff/UTBPanel#readme'
  const connect = `claude mcp add --transport http tgpanel ${endpoint}/mcp --header "Authorization: Bearer ${token}"`
  const upload = $derived(
    `curl -H "X-Panel: 1" -H "Authorization: Bearer ${token}" -F file=@bot ${endpoint}/api/bots/${botId || '<id>'}/releases`,
  )

  async function load() {
    try {
      owner = (await api.get<{ kind: string }>('/api/ping')).kind === 'owner'
      if (owner) {
        groups = await api.get<Group[]>('/api/mcp/groups')
        draft = Object.fromEntries(groups.map((g) => [g.id, g.mcp_mode])) as Record<number, Mode>
      }
    } catch (e) {
      error = errorText(e)
    } finally {
      loaded = true
    }
  }
  $effect(() => {
    load()
  })
  async function save(group: Group) {
    saving = group.id
    error = ''
    message = ''
    try {
      await api.put(`/api/mcp/groups/${group.id}`, { mode: draft[group.id] })
      group.mcp_mode = draft[group.id]
      message = `Режим группы «${group.name}» сохранён`
    } catch (e) {
      error = errorText(e)
    } finally {
      saving = 0
    }
  }
  async function copy(text: string) {
    error = ''
    try {
      await navigator.clipboard.writeText(text)
      message = 'Команда скопирована: замените текст в угловых скобках токеном из меню gate'
    } catch {
      error = 'Не удалось скопировать. Выделите команду вручную.'
    }
  }
</script>

<h1>Нейросети</h1>
{#if error}<p class="danger">{error}</p>{/if}
{#if message}<p class="ok">{message}</p>{/if}

{#if owner}
  <Box title="Подтверждения">
    <p class="muted">
      Опасные действия нейросеть только запрашивает: запрос ждёт здесь, в ленте уведомлений и в окне gate 15 минут.
      Подтверждение — удержанием кнопки.
    </p>
    <PendingActions history empty="Запросов нет." />
  </Box>
  <Box title="Запросы секретов">
    <p class="muted">
      Значения секретов и токены нейросеть не передаёт: она просит, вы вводите здесь или во вкладке «Настройки» бота.
      Нейросеть видит только состояние. Запрос живёт 24 часа.
    </p>
    <SecretRequests empty="Запросов нет." />
  </Box>
{/if}

<div class="cols">
  <Box title="Подключение MCP">
    <p>Запустите gate и скопируйте токен MCP в его меню (значок в трее). Вставьте токен в команду:</p>
    <pre>{connect}</pre>
    <button onclick={() => copy(connect)}>Скопировать команду</button>
    <p class="muted">
      Для другого клиента: Streamable HTTP, адрес {endpoint}/mcp, заголовок Authorization: Bearer с вашим токеном.
      Доступ связан с ключом этого устройства; «Токен для MCP: заменить» в меню gate отключает старый.
    </p>
  </Box>

  <Box title="Загрузка версии">
    <p>Локальная сборка слишком велика для вызова MCP — её загружают обычным запросом через gate с тем же токеном:</p>
    <label class="bot">
      Бот
      <select bind:value={botId}>
        <option value={0}>— выберите —</option>
        {#each store.bots as b (b.id)}<option value={b.id}>{b.name}</option>{/each}
      </select>
    </label>
    <pre>{upload}</pre>
    <button onclick={() => copy(upload)}>Скопировать команду</button>
    <p class="muted">
      Сборка: <code>CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bot</code>. Загрузка только добавляет версию (нужен
      полный доступ группы), деплой — отдельно, через deploy_release с вашим подтверждением.
    </p>
  </Box>
</div>

<Box title="Документация для нейросети">
  <ul class="links">
    <li>
      <a href="/llms.txt" target="_blank" rel="noreferrer">/llms.txt</a> — коротко о панели и список инструментов с уровнями.
    </li>
    <li>
      <a href="/docs/ai.md" target="_blank" rel="noreferrer">/docs/ai.md</a> — понятия, все инструменты с аргументами, схема
      документа настроек.
    </li>
    <li>
      <a href={readme} target="_blank" rel="noreferrer">README</a> — установка и «Как писать бота под панель».
    </li>
  </ul>
  <p class="muted">Оба файла собираются из описания API и всегда актуальны. С токеном MCP их можно читать через gate.</p>
</Box>

<Box title="Доступ по группам">
  <p class="muted">
    Выкл — группа недоступна через MCP. Только чтение — просмотр и диагностика. Полный — также изменения; опасные
    действия требуют подтверждения владельца.
  </p>
  <p class="muted">
    Боты без группы доступны только для чтения. Метрики сервера и аудит — только для чтения. Новый бот создаётся в
    группе с полным доступом.
  </p>
  {#if loaded && !owner}<p>Менять режимы может владелец ключа.</p>
  {:else if owner}
    <table>
      <thead><tr><th>Группа</th><th>Режим MCP</th><th></th></tr></thead>
      <tbody>
        {#each groups as group (group.id)}
          <tr>
            <td>{group.name}</td>
            <td>
              <select aria-label={`Режим MCP: ${group.name}`} bind:value={draft[group.id]} disabled={saving !== 0}>
                <option value="off">Выкл</option><option value="read">Только чтение</option><option value="full"
                  >Полный</option
                >
              </select>
            </td>
            <td>
              {#if draft[group.id] === 'full' && group.mcp_mode !== 'full'}
                <HoldButton onconfirm={() => save(group)} disabled={saving !== 0}>Включить полный доступ</HoldButton>
              {:else}<button
                  onclick={() => save(group)}
                  disabled={saving !== 0 || draft[group.id] === group.mcp_mode}>Сохранить</button
                >{/if}
            </td>
          </tr>
        {:else}<tr><td colspan="3" class="muted">Создайте группы на главной странице.</td></tr>{/each}
      </tbody>
    </table>
  {:else}<p class="muted">Загрузка…</p>{/if}
</Box>

<style>
  pre {
    overflow-x: auto;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  td {
    vertical-align: middle;
  }
  .cols {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: var(--s3);
  }
  .bot {
    display: flex;
    gap: var(--s2);
    align-items: center;
    margin-bottom: var(--s2);
  }
  .links {
    margin: 0 0 var(--s2);
    padding-left: var(--s3);
  }
</style>
