<script lang="ts">
  import type { BotView } from '../../lib/api/client'
  let { bot }: { bot: BotView } = $props()
</script>

<p class="muted">Настройки бота {bot.name} появятся в следующем шаге.</p>
