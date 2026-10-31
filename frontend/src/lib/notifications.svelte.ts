import type { Item } from './ui/Feed.svelte'

// The notification feed. Events arrive from the panel once notifications
// exist; the bell counts unread ones.
class Notifications {
  items = $state<Item[]>([])
  unread = $derived(this.items.filter((i) => !i.read).length)

  add(i: Item) {
    if (this.items.some((x) => x.id === i.id)) return
    this.items = [i, ...this.items].slice(0, 200)
  }

  read(id: number) {
    const i = this.items.find((x) => x.id === id)
    if (i) i.read = true
  }
}

export const notifications = new Notifications()
