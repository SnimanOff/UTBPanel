<script lang="ts">
  // Payments of the bot. Stars are refunded here (refundStarPayment) by
  // holding the button; other currencies — at the payment provider.
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import { utc } from '../../lib/format'
  import Box from '../../lib/ui/Box.svelte'
  import HoldButton from '../../lib/ui/HoldButton.svelte'

  let { botId, user = $bindable(0) }: { botId: number; user?: number } = $props()
  type Payment = Schemas['Payment']
  let list = $state<Payment[]>([])
  let error = $state('')

  async function load() {
    error = ''
    try {
      const p = new URLSearchParams({ limit: '100' })
      if (user) p.set('user', String(user))
      list = await api.get<Payment[]>(`/api/bots/${botId}/payments?${p}`)
    } catch (e) {
      error = errorText(e)
    }
  }
  $effect(() => {
    void [botId, user]
    load()
  })

  async function refund(p: Payment) {
    error = ''
    try {
      const done = await api.post<Payment>(`/api/bots/${botId}/payments/${p.id}/refund`)
      list = list.map((x) => (x.id === done.id ? done : x))
    } catch (e) {
      error = errorText(e)
    }
  }
  const money = (p: Payment) => (p.currency === 'XTR' ? `${p.amount} ★` : `${(p.amount / 100).toFixed(2)} ${p.currency}`)
</script>

<Box title="Платежи">
  {#snippet actions()}
    {#if user}
      <span class="muted">пользователь <span class="num">{user}</span></span>
      <button onclick={() => (user = 0)}>все</button>
    {/if}
  {/snippet}
  {#if error}<p class="danger">{error}</p>{/if}
  <table>
    <thead><tr><th>Время</th><th>Пользователь</th><th>Сумма</th><th>Payload</th><th></th></tr></thead>
    <tbody>
      {#each list as p (p.id)}
        <tr>
          <td class="num muted">{utc(p.ts)}</td>
          <td class="num"><button class="link" onclick={() => (user = p.user_id)}>{p.user_id}</button></td>
          <td class="num">{money(p)}</td>
          <td class="muted payload" title={p.payload}>{p.payload || '—'}</td>
          <td class="acts">
            {#if p.refunded_at}
              <span class="muted">возвращён {utc(p.refunded_at)}</span>
            {:else if p.refundable}
              <HoldButton onconfirm={() => refund(p)} title="Вернуть звёзды пользователю">Вернуть</HoldButton>
            {:else}
              <span class="muted" title="Bot API возвращает только Stars">возврат — у провайдера</span>
            {/if}
          </td>
        </tr>
      {:else}
        <tr><td colspan="5" class="muted">Платежей нет</td></tr>
      {/each}
    </tbody>
  </table>
  <p class="muted small">Бот узнаёт о возврате из апдейта refunded_payment и сам забирает товар.</p>
</Box>

<style>
  table { width: 100%; }
  th, td { text-align: left; padding: var(--s1) var(--s2); border-bottom: var(--line); }
  th { color: var(--muted); font-weight: 400; font-size: var(--fs-s); }
  .payload { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .acts { white-space: nowrap; }
  .link { border: none; background: none; padding: 0; color: var(--accent); cursor: pointer; }
  .small { font-size: var(--fs-s); }
  @media (max-width: 700px) { .payload { display: none; } }
</style>
