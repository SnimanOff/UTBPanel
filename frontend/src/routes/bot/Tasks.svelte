<script lang="ts">
  import { api, errorText, type Schemas } from '../../lib/api/client'
  import Box from '../../lib/ui/Box.svelte'
  let { botId, runnable }: { botId: number; runnable: boolean } = $props()
  let data = $state<Schemas['SDKTasksView']>()
  let error = $state('')
  let busy = $state(false)
  async function load() {
    try { data = await api.get<Schemas['SDKTasksView']>(`/api/bots/${botId}/sdk/tasks`); error = '' }
    catch (e) { error = errorText(e) }
  }
  $effect(() => { void botId; load(); const timer = setInterval(load, 5000); return () => clearInterval(timer) })
  async function run(name: string) {
    busy = true
    try { await api.post(`/api/bots/${botId}/sdk/tasks/${encodeURIComponent(name)}/run`); await load() }
    catch (e) { error = errorText(e) }
    finally { busy = false }
  }
</script>

<Box title="Задачи">
  <p class="muted">Расписание из кода бота, время UTC. Во время паузы задачи не выполняются.</p>
  {#if error}<p class="danger">{error}</p>{/if}
  {#if data}
    <table><tbody>{#each data.tasks as task (task.name)}
      <tr><td>{task.name}</td><td class="mono">{task.cron}</td><td><button disabled={busy || !runnable} onclick={() => run(task.name)}>Запустить сейчас</button></td></tr>
    {:else}<tr><td class="muted">Задачи не объявлены</td></tr>{/each}</tbody></table>
    <h3>Последние запуски</h3>
    <table><tbody>{#each data.runs as job (job.id)}
      <tr><td class="num">{job.started.slice(0, 19).replace('T', ' ')}</td><td>{job.name}</td><td>{job.source === 'cron' ? 'по расписанию' : 'вручную'}</td><td class:danger={job.finished && !job.success}>{!job.finished ? 'выполняется' : job.success ? 'готово' : job.error}</td><td class="num">{job.finished ? `${job.duration_ms.toFixed(0)} мс` : '—'}</td></tr>
    {/each}</tbody></table>
  {/if}
</Box>
