// Minimal fetch wrapper for the JSON API.
export class ApiError extends Error {
  status: number
  data: Record<string, unknown>
  constructor(status: number, message: string, data: Record<string, unknown> = {}) {
    super(message)
    this.status = status
    this.data = data
  }
}

async function request<T>(method: string, url: string, body?: unknown, raw = false): Promise<T> {
  const init: RequestInit = { method, headers: {}, credentials: 'same-origin' }
  if (body !== undefined) {
    ;(init.headers as Record<string, string>)['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  const res = await fetch(url, init)
  if (res.status === 401 && !url.includes('/auth/')) {
    window.dispatchEvent(new CustomEvent('pc:unauthorized'))
  }
  if (!res.ok) {
    let msg = res.statusText
    let data: Record<string, unknown> = {}
    try {
      data = await res.json()
      msg = (data.error as string) || msg
    } catch {}
    throw new ApiError(res.status, msg, data)
  }
  if (raw) return (await res.text()) as unknown as T
  if (res.status === 204) return undefined as T
  return res.json()
}

export const api = {
  get: <T>(url: string) => request<T>('GET', url),
  getText: (url: string) => request<string>('GET', url, undefined, true),
  post: <T>(url: string, body?: unknown) => request<T>('POST', url, body),
  put: <T>(url: string, body?: unknown) => request<T>('PUT', url, body),
  del: <T>(url: string) => request<T>('DELETE', url),
}

export const V1 = '/api/v1'

/** POST a file as multipart/form-data. */
export async function upload<T>(url: string, field: string, file: File): Promise<T> {
  const fd = new FormData()
  fd.append(field, file)
  const res = await fetch(url, { method: 'POST', body: fd, credentials: 'same-origin' })
  const j = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(res.status, j.error || res.statusText, j)
  return j as T
}

/** POST multipart with extra fields. */
export async function uploadForm<T>(url: string, file: File, fields: Record<string, string> = {}): Promise<T> {
  const fd = new FormData()
  fd.append('file', file)
  for (const [k, v] of Object.entries(fields)) fd.append(k, v)
  const res = await fetch(url, { method: 'POST', body: fd, credentials: 'same-origin' })
  const j = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(res.status, j.error || res.statusText, j)
  return j as T
}
