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

<div class="wrap">
  <div class="secret">
    <span class="name mono" title={name}>{name}</span>
    <input
      type="password"
      autocomplete="off"
      aria-label={name}
      title={isSet ? 'Значение задано. Введите новое, чтобы заменить' : undefined}
      placeholder={isSet ? 'задан · новое заменит' : 'не задан'}
      bind:value
      onkeydown={(e) => e.key === 'Enter' && save()}
    />
    <button disabled={!value || saving} onclick={save}>Сохранить</button>
  </div>
  {#if error}<div class="error">{error}</div>{/if}
</div>

<style>
  /* Sized by the box it sits in, not the window: the token box is half
     the page wide, the secrets list is full width. */
  .wrap {
    container-type: inline-size;
  }
  .secret {
    display: grid;
    grid-template-columns: 200px minmax(0, 1fr) auto;
    gap: var(--s2);
    align-items: center;
  }
  .secret input {
    min-width: 0;
    width: 100%;
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .error {
    color: var(--danger-text);
    font-size: var(--fs-s);
    margin: 2px 0 var(--s2) 208px;
    white-space: pre-wrap;
  }

  /* Narrow box: the name above, the field and the button below. */
  @container (max-width: 560px) {
    .secret {
      grid-template-columns: minmax(0, 1fr) auto;
      row-gap: var(--s1);
    }
    .name {
      grid-column: 1 / -1;
    }
    .error {
      margin-left: 0;
    }
  }

  /* Very narrow: everything in one column. */
  @container (max-width: 320px) {
    .secret {
      grid-template-columns: 1fr;
    }
  }
</style>
