// Times in forms are entered in UTC; next to them the bot's zone is shown
// the way users will read it: "18:00 UTC = 21:00 МСК".

export const defaultTZ = 'Europe/Moscow'

const abbrs: Record<string, string> = {
  'Europe/Moscow': 'МСК',
  'Europe/Kaliningrad': 'КЛД',
  'Europe/Samara': 'САМТ',
  'Asia/Yekaterinburg': 'ЕКБ',
  'Asia/Novosibirsk': 'НСК',
  'Asia/Vladivostok': 'ВЛАД',
  UTC: 'UTC',
}

function parts(d: Date, tz: string) {
  const f = new Intl.DateTimeFormat('ru-RU', {
    timeZone: tz, day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  })
  const p = Object.fromEntries(f.formatToParts(d).map((x) => [x.type, x.value]))
  return { date: `${p.day}.${p.month}`, time: `${p.hour}:${p.minute}` }
}

export function abbr(tz: string, d = new Date()): string {
  if (abbrs[tz]) return abbrs[tz]
  const n = new Intl.DateTimeFormat('en-US', { timeZone: tz, timeZoneName: 'short' })
    .formatToParts(d).find((x) => x.type === 'timeZoneName')
  return n?.value ?? tz
}

export function pair(d: Date, tz = defaultTZ): string {
  const u = parts(d, 'UTC')
  const s = `${u.time} UTC`
  if (tz === 'UTC') return s
  const l = parts(d, tz)
  return `${s} = ${l.date === u.date ? l.time : `${l.date} ${l.time}`} ${abbr(tz, d)}`
}

// Zones offered in settings; any IANA name can be typed.
export const zones = [
  'Europe/Moscow', 'Europe/Kaliningrad', 'Europe/Samara', 'Asia/Yekaterinburg', 'Asia/Omsk',
  'Asia/Novosibirsk', 'Asia/Krasnoyarsk', 'Asia/Irkutsk', 'Asia/Yakutsk', 'Asia/Vladivostok',
  'Asia/Magadan', 'Asia/Kamchatka', 'Europe/Minsk', 'Europe/Kyiv', 'Asia/Almaty', 'Asia/Tashkent',
  'Europe/Berlin', 'Europe/London', 'UTC',
]

export function validZone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: tz })
    return true
  } catch {
    return false
  }
}
