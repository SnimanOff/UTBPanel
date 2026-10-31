// Formatting helpers. All times in the panel are UTC.

export function uptime(fromIso?: string): string {
  if (!fromIso) return '—'
  const s = Math.max(0, Math.floor((Date.now() - new Date(fromIso).getTime()) / 1000))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}д ${h}ч`
  if (h > 0) return `${h}ч ${m}м`
  if (m > 0) return `${m}м`
  return `${s}с`
}

export function utc(iso?: string, withSeconds = false): string {
  if (!iso) return '—'
  const s = new Date(iso).toISOString().replace('T', ' ')
  return s.slice(0, withSeconds ? 19 : 16)
}

export function bytes(n?: number): string {
  if (n === undefined || n === null) return '—'
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' ГБ'
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' МБ'
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(0) + ' КБ'
  return n + ' Б'
}

export function compact(n: number): string {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k'
  return String(n)
}
