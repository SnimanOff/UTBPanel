<script lang="ts">
  // A write-only secret: the value is never shown or read back. The field is
  // empty; "задан" tells that a value exists.
  let {
    name,
    isSet = false,
    onsave,
    error = '',
  }: { name: string; isSet?: boolean; onsave: (value: string) => Promise<void>; error?: string } = $props()
  let value = $state('')
  let saving = $state(false)

  async function save() {
    if (!value) return
    saving = true
    try {
      await onsave(value)
      value = ''
    } finally {
      saving = false
    }
  }
</script>

<div class="secret">
  <span class="name mono">{name}</span>
  <input
    type="password"
    autocomplete="off"
    placeholder={isSet ? 'задан — введите новое значение, чтобы заменить' : 'не задан'}
    bind:value
    onkeydown={(e) => e.key === 'Enter' && save()}
  />
  <button disabled={!value || saving} onclick={save}>Сохранить</button>
</div>
{#if error}<div class="error">{error}</div>{/if}

<style>
  .secret {
    display: grid;
    grid-template-columns: 200px 1fr auto;
    gap: var(--s2);
    align-items: center;
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .error {
    color: var(--danger);
    font-size: var(--fs-s);
    margin: 2px 0 var(--s2) 208px;
    white-space: pre-wrap;
  }
</style>
