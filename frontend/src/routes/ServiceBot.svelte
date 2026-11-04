<script lang="ts">
  // Service bot: notifications of the feed go to the owner's Telegram.
  import { api, errorText, type Schemas } from '../lib/api/client'
  import Box from '../lib/ui/Box.svelte'
  import HoldButton from '../lib/ui/HoldButton.svelte'

  type State = Schemas['ServiceBot']
  let bot = $state<State>()
  let token = $state('')
  let busy = $state(false)
  let error = $state(''), message = $state('')

  async function load() {
    try { bot = await api.get<State>('/api/server/service-bot') } catch (e) { error = errorText(e) }
  }
  $effect(() => { load() })

  async function save(body: { token?: string; level?: string }) {
    busy = true; error = ''; message = ''
    try {
      bot = await api.put<State>('/api/server/service-bot', body)
      if (body.token) { token = ''; message = `Подключён @${bot.username}. Откройте его в Telegram, нажмите Start и проверьте.` }
    } catch (e) { error = errorText(e) } finally { busy = false }
  }
  async function test() {
    busy = true; error = ''; message = ''
    try { await api.post('/api/server/service-bot/test'); message = 'Пробное сообщение отправлено' }
    catch (e) { error = errorText(e) } finally { busy = false }
  }
  async function remove() {
    error = ''; message = ''
    try { await api.del('/api/server/service-bot'); await load() } catch (e) { error = errorText(e) }
  }
</script>

<Box title="Сервисный бот">
  <p class="muted">Отдельный бот от BotFather, который пишет вам в Telegram о падениях, откатах и порогах. Команд он не принимает. Токен хранится в ядре зашифрованным.</p>
  {#if bot?.configured}
    <div class="row">
      <span>@{bot.username}</span>
      <label>Присылать
        <select value={bot.level} disabled={busy} onchange={(e) => save({ level: (e.currentTarget as HTMLSelectElement).value })}>
          <option value="errors">только ошибки</option>
          <option value="warnings">ошибки и предупреждения</option>
        </select>
      </label>
      <button onclick={test} disabled={busy || !bot.owner_tg_id}>Проверить</button>
      <HoldButton onconfirm={remove}>Отключить</HoldButton>
    </div>
    {#if !bot.owner_tg_id}<p class="danger">Укажите свой Telegram ID в настройках панели: иначе сообщения отправлять некому.</p>{/if}
  {/if}
  <div class="row">
    <input class="mono grow" type="password" autocomplete="off" placeholder={bot?.configured ? 'новый токен, чтобы заменить' : 'токен от BotFather'} bind:value={token} disabled={busy} />
    <button class="primary" onclick={() => save({ token })} disabled={busy || !token.trim()}>{bot?.configured ? 'Заменить' : 'Подключить'}</button>
  </div>
  {#if error}<p class="danger">{error}</p>{/if}
  {#if message}<p>{message}</p>{/if}
</Box>

<style>
  .row { display: flex; gap: var(--s3); align-items: center; flex-wrap: wrap; margin-bottom: var(--s3); }
  .grow { flex: 1; min-width: 220px; }
</style>
