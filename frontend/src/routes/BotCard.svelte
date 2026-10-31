<script lang="ts">
  import type { BotView } from '../lib/api/client'
  import { link } from '../lib/router.svelte'
  import StatusSquare from '../lib/ui/StatusSquare.svelte'

  let { bot }: { bot: BotView } = $props()
</script>

<a class="card" href="/bots/{bot.id}" use:link>
  <div class="row">
    <StatusSquare state={bot.state} />
    <span class="name">{bot.name}</span>
    <span class="muted user">@{bot.username}</span>
  </div>
</a>

<style>
  .card {
    display: block;
    background: var(--surface);
    border: var(--line);
    padding: var(--s3);
  }
  .card:hover {
    border-color: var(--accent);
  }
  .row {
    display: flex;
    align-items: center;
    gap: var(--s2);
  }
  .name {
    font-weight: 500;
  }
  .user {
    margin-left: auto;
  }
</style>
