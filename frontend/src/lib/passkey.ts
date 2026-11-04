// WebAuthn in the browser: the server sends options with base64url fields,
// the browser answers with ArrayBuffers that go back as base64url.

export function b64url(buf: ArrayBuffer): string {
  let s = ''
  for (const b of new Uint8Array(buf)) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

export function fromB64url(s: string): ArrayBuffer {
  const bin = atob(s.replace(/-/g, '+').replace(/_/g, '/'))
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out.buffer
}

export const supported = () => typeof window !== 'undefined' && !!window.PublicKeyCredential

/** Creates a passkey; returns the registration for /web/invite/{token}/finish. */
export async function create(o: any) {
  const cred = (await navigator.credentials.create({
    publicKey: {
      ...o,
      challenge: fromB64url(o.challenge),
      user: { ...o.user, id: fromB64url(o.user.id) },
    },
  })) as PublicKeyCredential | null
  if (!cred) throw new Error('ключ не создан')
  const r = cred.response as AuthenticatorAttestationResponse
  return { id: b64url(cred.rawId), client_data_json: b64url(r.clientDataJSON), attestation_object: b64url(r.attestationObject) }
}

/** Signs in; returns the assertion for /web/login/finish. */
export async function get(o: any) {
  const cred = (await navigator.credentials.get({
    publicKey: { ...o, challenge: fromB64url(o.challenge) },
  })) as PublicKeyCredential | null
  if (!cred) throw new Error('вход отменён')
  const r = cred.response as AuthenticatorAssertionResponse
  return {
    id: b64url(cred.rawId),
    client_data_json: b64url(r.clientDataJSON),
    authenticator_data: b64url(r.authenticatorData),
    signature: b64url(r.signature),
  }
}

/** Human text for errors of the browser's passkey dialog. */
export function passkeyError(e: unknown): string {
  if (e instanceof DOMException) {
    if (e.name === 'NotAllowedError') return 'Отменено или время вышло. Попробуйте ещё раз.'
    if (e.name === 'InvalidStateError') return 'На этом устройстве уже есть такой ключ.'
    if (e.name === 'SecurityError') return 'Браузер не доверяет адресу: откройте панель по домену с https.'
  }
  return e instanceof Error ? e.message : String(e)
}
