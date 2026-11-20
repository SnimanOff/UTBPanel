<script lang="ts">
  import type { Bot } from '../api/client'
  import { session } from '../session.svelte'

  let { bot, disabled, onaction }: {
    bot: Bot
    disabled: boolean
    onaction: (action: 'pause' | 'resume') => void
  } = $props()

  const active = $derived(bot.maintenance_state === 'active' || (!bot.maintenance_state && !!bot.maintenance && bot.state === 'paused'))
  const planned = $derived(!active && (bot.maintenance_state === 'planned' || !!bot.maintenance))
  const stopped = $derived(!active && bot.state === 'stopped')
</script>

{#if session.scope?.role !== 'view'}
  <button
    type="button"
    disabled={disabled || planned || stopped}
    title={planned ? 'Чтобы начать сейчас, отмените план во вкладке «Деплой»' : stopped ? 'Сначала запустите бота' : undefined}
    onclick={() => onaction(active ? 'resume' : 'pause')}
  >{active ? 'Завершить техработы' : 'Начать техработы'}</button>
{/if}
