<script lang="ts">
  import { api, errorText, type Group } from '../lib/api/client'
  import Box from '../lib/ui/Box.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'
  import PendingActions from '../lib/ui/PendingActions.svelte'
  import SecretRequests from '../lib/ui/SecretRequests.svelte'

  type Mode = 'off' | 'read' | 'full'
  let groups = $state<Group[]>([])
  let draft = $state<Record<number, Mode>>({})
  let owner = $state(false), loaded = $state(false), saving = $state(0)
  let error = $state(''), message = $state('')
  const command = 'claude mcp add --transport http tgpanel http://127.0.0.1:47100/mcp --header "Authorization: Bearer <токен из меню gate>"'

  async function load() {
    try {
      owner = (await api.get<{ kind: string }>('/api/ping')).kind === 'owner'
      if (owner) {
        groups = await api.get<Group[]>('/api/mcp/groups')
        draft = Object.fromEntries(groups.map(g => [g.id, g.mcp_mode])) as Record<number, Mode>
      }
    } catch (e) { error = errorText(e) }
    finally { loaded = true }
  }
  $effect(() => { load() })
  async function save(group: Group) {
    saving = group.id; error = ''; message = ''
    try {
      await api.put(`/api/mcp/groups/${group.id}`, { mode: draft[group.id] })
      group.mcp_mode = draft[group.id]
      message = `Режим группы «${group.name}» сохранён`
    } catch (e) { error = errorText(e) }
    finally { saving = 0 }
  }
  async function copy() {
    try { await navigator.clipboard.writeText(command); message = 'Команда скопирована: замените текст в угловых скобках токеном из меню gate' }
    catch { error = 'Не удалось скопировать. Выделите команду вручную.' }
  }
</script>

<h1>Нейросети</h1>
{#if owner}
  <Box title="Подтверждения">
    <p class="muted">Опасные действия нейросеть только запрашивает: запрос ждёт здесь, в ленте уведомлений и в окне gate 15 минут. Подтверждение — удержанием кнопки.</p>
    <PendingActions history />
  </Box>
  <Box title="Запросы секретов">
    <p class="muted">Значения секретов и токены нейросеть не передаёт: она просит, вы вводите здесь или во вкладке «Настройки» бота. Нейросеть видит только состояние. Запрос живёт 24 часа.</p>
    <SecretRequests />
  </Box>
{/if}
<Box title="Подключение MCP">
  <p>Запустите gate и скопируйте токен MCP в его меню. Вставьте токен в команду:</p>
  <pre>{command}</pre>
  <button onclick={copy}>Скопировать команду</button>
  <p class="muted">Для другого клиента: Streamable HTTP, адрес http://127.0.0.1:47100/mcp, заголовок Authorization: Bearer с вашим токеном. Доступ связан с ключом устройства.</p>
</Box>

<Box title="Доступ по группам">
  <p class="muted">Выкл — группа недоступна через MCP. Только чтение — просмотр и диагностика. Полный — также изменения; опасные действия требуют подтверждения владельца.</p>
  <p class="muted">Боты без группы доступны только для чтения. Метрики сервера и аудит — только для чтения. Новый бот создаётся в группе с полным доступом.</p>
  {#if loaded && !owner}<p>Менять режимы может владелец ключа.</p>
  {:else if owner}
    <table>
      <thead><tr><th>Группа</th><th>Режим MCP</th><th></th></tr></thead>
      <tbody>
        {#each groups as group (group.id)}
          <tr>
            <td>{group.name}</td>
            <td><select aria-label={`Режим MCP: ${group.name}`} bind:value={draft[group.id]} disabled={saving !== 0}>
              <option value="off">Выкл</option><option value="read">Только чтение</option><option value="full">Полный</option>
            </select></td>
            <td>{#if draft[group.id] === 'full' && group.mcp_mode !== 'full'}
              <HoldButton onconfirm={() => save(group)} disabled={saving !== 0}>Включить полный доступ</HoldButton>
            {:else}<button onclick={() => save(group)} disabled={saving !== 0 || draft[group.id] === group.mcp_mode}>Сохранить</button>{/if}</td>
          </tr>
        {:else}<tr><td colspan="3" class="muted">Создайте группы на главной странице.</td></tr>{/each}
      </tbody>
    </table>
  {:else}<p class="muted">Загрузка…</p>{/if}
</Box>
{#if error}<p class="danger">{error}</p>{/if}
{#if message}<p>{message}</p>{/if}

<style>
  pre { overflow-x: auto; white-space: pre-wrap; overflow-wrap: anywhere; }
  td { vertical-align: middle; }
</style>
