import type { components } from './schema'

export type Schemas = components['schemas']
export type Bot = Schemas['Bot']
export type BotView = Schemas['BotView']
export type Status = Schemas['Status']
export type Group = Schemas['Group']
export type LogLine = Schemas['LogLine']
export type Release = Schemas['Release']
export type SecretInfo = Schemas['SecretInfo']
export type AuditEntry = Schemas['AuditEntry']

/** The single error format of the API. Telegram's answer is passed as is. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public telegram?: unknown,
  ) {
    super(message)
  }
}

type Body = BodyInit | object | undefined

async function request<T>(method: string, path: string, body?: Body): Promise<T> {
  const headers: Record<string, string> = { 'X-Panel': '1' }
  let payload: BodyInit | undefined
  if (body instanceof FormData || body instanceof Blob || typeof body === 'string') {
    payload = body
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  let resp: Response
  try {
    resp = await fetch(path, { method, headers, body: payload, credentials: 'same-origin' })
  } catch (e) {
    throw new ApiError(0, 'network', 'нет связи с gate')
  }
  if (resp.status === 204) return undefined as T
  const text = await resp.text()
  let data: any
  try {
    data = text ? JSON.parse(text) : undefined
  } catch {
    data = undefined
  }
  if (!resp.ok) {
    if (data && typeof data.code === 'string') {
      throw new ApiError(resp.status, data.code, data.message, data.telegram)
    }
    throw new ApiError(resp.status, 'http', text.slice(0, 300) || resp.statusText)
  }
  return data as T
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: Body) => request<T>('POST', path, body ?? undefined),
  put: <T>(path: string, body?: Body) => request<T>('PUT', path, body),
  patch: <T>(path: string, body?: Body) => request<T>('PATCH', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}

/** Progress of an upload: bytes sent so far of the total. */
export type UploadProgress = { loaded: number; total: number }

/**
 * POST with upload progress. fetch has no upload progress events, so this is
 * XMLHttpRequest; errors are the same ApiError as from request().
 */
export function uploadWithProgress<T>(path: string, body: FormData, onprogress: (p: UploadProgress) => void, signal?: AbortSignal): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', path)
    xhr.withCredentials = true
    xhr.setRequestHeader('X-Panel', '1')
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onprogress({ loaded: e.loaded, total: e.total })
    }
    xhr.onerror = () => reject(new ApiError(0, 'network', 'нет связи с gate'))
    xhr.onabort = () => reject(new ApiError(0, 'aborted', 'загрузка отменена'))
    xhr.onload = () => {
      if (xhr.status === 204) return resolve(undefined as T)
      const text = xhr.responseText
      let data: any
      try {
        data = text ? JSON.parse(text) : undefined
      } catch {
        data = undefined
      }
      if (xhr.status < 200 || xhr.status >= 300) {
        if (data && typeof data.code === 'string') {
          return reject(new ApiError(xhr.status, data.code, data.message, data.telegram))
        }
        return reject(new ApiError(xhr.status, 'http', text.slice(0, 300) || xhr.statusText))
      }
      resolve(data as T)
    }
    signal?.addEventListener('abort', () => xhr.abort(), { once: true })
    xhr.send(body)
  })
}

/** Human-readable text of any error. */
export function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === 'core_unavailable') return 'Ядро не запущено'
    return e.message
  }
  return String(e)
}

/**
 * Server-Sent Events with reconnects. The browser sends Last-Event-ID by
 * itself after a reconnect; on a hard error we reopen after a pause.
 */
export function subscribe(
  path: string,
  handlers: Record<string, (data: any) => void>,
  onState?: (open: boolean) => void,
): () => void {
  let es: EventSource | undefined
  let closed = false
  let timer: ReturnType<typeof setTimeout> | undefined
  let delay = 1000
  const open = () => {
    es = new EventSource(path)
    es.onopen = () => {
      delay = 1000
      onState?.(true)
    }
    es.onerror = () => {
      onState?.(false)
      if (es?.readyState === EventSource.CLOSED && !closed) {
        timer = setTimeout(open, delay)
        delay = Math.min(delay * 2, 15000)
      }
    }
    for (const [name, fn] of Object.entries(handlers)) {
      es.addEventListener(name, (ev) => {
        try {
          fn(JSON.parse((ev as MessageEvent).data))
        } catch {
          /* a broken frame is skipped */
        }
      })
    }
  }
  open()
  return () => {
    closed = true
    clearTimeout(timer)
    es?.close()
  }
}
