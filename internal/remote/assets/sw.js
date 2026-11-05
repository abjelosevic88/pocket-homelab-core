// Pocket remote access: hands every request of the app frame to the shell
// page, which sends it through the encrypted session to Core. The shell's
// own files and top-level pages still come from the relay.
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', e => e.waitUntil(self.clients.claim()))

self.addEventListener('fetch', e => {
  const url = new URL(e.request.url)
  if (url.origin !== self.location.origin) return
  if (url.pathname.startsWith('/__pocket/')) return
  if (e.request.mode === 'navigate' && e.request.destination === 'document') return
  e.respondWith(tunnel(e.request, url))
})

async function shell() {
  const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
  const tops = all.filter(c => c.frameType === 'top-level' && new URL(c.url).origin === self.location.origin)
  return tops.find(c => c.focused) || tops.find(c => c.visibilityState === 'visible') || tops[0]
}

const NULL_BODY = new Set([101, 204, 205, 304])

async function tunnel(req, url) {
  const target = await shell()
  if (!target) return new Response('Pocket remote access: the page that holds the connection is gone. Reload.', { status: 503 })
  const body = req.method === 'GET' || req.method === 'HEAD' ? null : await req.arrayBuffer()
  const headers = []
  req.headers.forEach((v, k) => headers.push([k, v]))
  const ch = new MessageChannel()
  const html = req.destination === 'iframe' || req.destination === 'frame'
  const msg = { type: 'pocket-fetch', method: req.method, url: url.pathname + url.search, headers, body }
  target.postMessage(msg, body ? [ch.port2, body] : [ch.port2])
  return new Promise(resolve => {
    let ctrl = null
    let pending = []
    let done = false
    const port = ch.port1
    port.onmessage = ({ data: m }) => {
      if (m.t === 'head') {
        const h = new Headers(m.headers)
        if (NULL_BODY.has(m.status) || req.method === 'HEAD') { resolve(new Response(null, { status: m.status, headers: h })); port.postMessage({ t: 'drain' }); return }
        const ct = h.get('content-type') || ''
        if (html && ct.includes('text/html')) {
          // Collect the page and put inject.js first, then answer.
          const parts = []
          port.onmessage = ({ data: n }) => {
            if (n.t === 'chunk') parts.push(new Uint8Array(n.data))
            else if (n.t === 'end' || n.t === 'error') {
              const text = new TextDecoder().decode(concat(parts))
              h.delete('content-length')
              resolve(new Response(inject(text), { status: m.status, headers: h }))
            }
          }
          return
        }
        h.delete('content-length')
        const stream = new ReadableStream({
          start(c) { ctrl = c; for (const p of pending) c.enqueue(p); pending = null },
          cancel() { done = true; port.postMessage({ t: 'cancel' }) },
        })
        resolve(new Response(stream, { status: m.status, headers: h }))
      } else if (m.t === 'chunk') {
        if (done) return
        const b = new Uint8Array(m.data)
        if (ctrl) ctrl.enqueue(b); else pending.push(b)
      } else if (m.t === 'end') {
        done = true
        if (ctrl) ctrl.close()
      } else if (m.t === 'error') {
        if (!ctrl && pending !== null && !done) { done = true; resolve(new Response(m.message || 'Pocket remote access: request failed', { status: 502 })); return }
        done = true
        try { ctrl && ctrl.error(new TypeError(m.message || 'connection lost')) } catch {}
      }
    }
  })
}

function concat(parts) {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0))
  let o = 0
  for (const p of parts) { out.set(p, o); o += p.length }
  return out
}

const TAG = '<script src="/__pocket/inject.js"></script>'
function inject(html) {
  const m = /<head[^>]*>/i.exec(html)
  if (m) return html.slice(0, m.index + m[0].length) + TAG + html.slice(m.index + m[0].length)
  const d = /<!doctype[^>]*>/i.exec(html)
  if (d) return html.slice(0, d.index + d[0].length) + TAG + html.slice(d.index + d[0].length)
  return TAG + html
}
