// Pocket remote-access relay for Cloudflare Workers. Core uploads this file
// with the shell's files (as text modules) to the person's own account.
// It only moves frames between Core and browsers; it never sees plaintext.
// Same protocol as the Go relay in internal/remote/relay.go.
import { DurableObject } from 'cloudflare:workers'
import SHELL_HTML from './shell.html.txt'
import SHELL_JS from './shell.js.txt'
import SHELL_CSS from './shell.css.txt'
import SW_JS from './sw.js.txt'
import INJECT_JS from './inject.js.txt'
import NOISE_JS from './noise.js.txt'

const FILES = {
  'shell.js': [SHELL_JS, 'text/javascript; charset=utf-8'],
  'shell.css': [SHELL_CSS, 'text/css; charset=utf-8'],
  'sw.js': [SW_JS, 'text/javascript; charset=utf-8'],
  'inject.js': [INJECT_JS, 'text/javascript; charset=utf-8'],
  'noise.js': [NOISE_JS, 'text/javascript; charset=utf-8'],
}
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; frame-src 'self'; worker-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
const OPEN = 1, DATA = 2, CLOSE = 3
const MAX_CHANNELS = 256

function frame(type, id, payload) {
  const p = payload ? new Uint8Array(payload) : new Uint8Array(0)
  const b = new Uint8Array(5 + p.length)
  b[0] = type
  new DataView(b.buffer).setUint32(1, id, false)
  b.set(p, 5)
  return b
}

function asset(path) {
  const base = { 'X-Content-Type-Options': 'nosniff', 'Referrer-Policy': 'no-referrer', 'Cache-Control': 'no-cache' }
  if (path.startsWith('/__pocket/')) {
    const f = FILES[path.slice(10)]
    if (!f) return new Response('not found', { status: 404 })
    const h = { ...base, 'Content-Type': f[1] }
    if (path === '/__pocket/sw.js') h['Service-Worker-Allowed'] = '/'
    return new Response(f[0], { headers: h })
  }
  return new Response(SHELL_HTML, { headers: { ...base, 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store', 'Content-Security-Policy': CSP } })
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url)
    if (url.pathname === '/__pocket/box' || url.pathname === '/__pocket/tunnel' || url.pathname === '/__pocket/health') {
      const stub = env.RELAY.get(env.RELAY.idFromName('box'))
      return stub.fetch(request)
    }
    if (url.pathname === '/__pocket/config.json') {
      return Response.json({ portal: env.PORTAL || '' }, { headers: { 'Cache-Control': 'no-store' } })
    }
    if (request.method !== 'GET' && request.method !== 'HEAD') return new Response('method not allowed', { status: 405 })
    return asset(url.pathname)
  },
}

function safeEqual(a, b) {
  const x = new TextEncoder().encode(a), y = new TextEncoder().encode(b)
  if (x.length !== y.length) return false
  return crypto.subtle.timingSafeEqual(x, y)
}

// One Durable Object holds Core's socket and every browser socket. It uses
// WebSocket hibernation: idle sockets stay open without keeping it in memory.
export class Relay extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env)
    this.ctx.setWebSocketAutoResponse(new WebSocketRequestResponsePair('ping', 'pong'))
  }

  box() { return this.ctx.getWebSockets('box')[0] }

  async fetch(request) {
    const url = new URL(request.url)
    if (url.pathname === '/__pocket/health') return Response.json({ box: !!this.box() })
    if (request.headers.get('Upgrade') !== 'websocket') return new Response('expected a WebSocket', { status: 426 })
    const [client, server] = Object.values(new WebSocketPair())

    if (url.pathname === '/__pocket/box') {
      const tok = (request.headers.get('Authorization') || '').replace(/^Bearer\s+/, '')
      if (!this.env.BOX_SECRET || !safeEqual(tok, this.env.BOX_SECRET)) return new Response('unauthorized', { status: 401 })
      for (const old of this.ctx.getWebSockets('box')) { try { old.close(1008, 'replaced by a newer box connection') } catch {} }
      for (const ch of this.ctx.getWebSockets('chan')) { try { ch.close(1001, 'box reconnected') } catch {} }
      this.ctx.acceptWebSocket(server, ['box'])
      return new Response(null, { status: 101, webSocket: client })
    }

    // A browser. Same-origin only, like the Go relay.
    const origin = request.headers.get('Origin')
    if (origin && new URL(origin).host !== url.host) return new Response('forbidden', { status: 403 })
    const box = this.box()
    let id = 0
    do { id = crypto.getRandomValues(new Uint32Array(1))[0] || 1 } while (this.ctx.getWebSockets('c' + id).length)
    // Tags survive hibernation; plain variables do not.
    this.ctx.acceptWebSocket(server, ['chan', 'c' + id])
    if (!box) { server.close(1013, 'box offline'); return new Response(null, { status: 101, webSocket: client }) }
    if (this.ctx.getWebSockets('chan').length > MAX_CHANNELS) { server.close(1013, 'too many connections'); return new Response(null, { status: 101, webSocket: client }) }
    const ip = request.headers.get('CF-Connecting-IP') || ''
    try {
      box.send(frame(OPEN, id, new TextEncoder().encode(JSON.stringify({ host: url.hostname.toLowerCase(), ip }))))
    } catch { server.close(1013, 'box offline') }
    return new Response(null, { status: 101, webSocket: client })
  }

  chanId(ws) {
    const t = this.ctx.getTags(ws).find(t => /^c\d+$/.test(t))
    return t ? Number(t.slice(1)) : 0
  }

  async webSocketMessage(ws, msg) {
    if (typeof msg === 'string') return
    const tags = this.ctx.getTags(ws)
    if (tags.includes('box')) {
      const b = new Uint8Array(msg)
      if (b.length < 5) return
      const type = b[0], id = new DataView(b.buffer, b.byteOffset).getUint32(1, false)
      const ch = this.ctx.getWebSockets('c' + id)[0]
      if (!ch) return
      if (type === DATA) { try { ch.send(b.subarray(5)) } catch {} }
      else if (type === CLOSE) { try { ch.close(1000, '') } catch {} }
      return
    }
    const id = this.chanId(ws), box = this.box()
    if (!id) return
    if (!box) { ws.close(1013, 'box offline'); return }
    try { box.send(frame(DATA, id, msg)) } catch { ws.close(1013, 'box offline') }
  }

  async webSocketClose(ws, code, reason) {
    const tags = this.ctx.getTags(ws)
    if (tags.includes('box')) {
      for (const ch of this.ctx.getWebSockets('chan')) { try { ch.close(1013, 'box offline') } catch {} }
      return
    }
    const id = this.chanId(ws), box = this.box()
    if (id && box) { try { box.send(frame(CLOSE, id)) } catch {} }
    try { ws.close(code === 1005 ? 1000 : code, reason) } catch {}
  }

  async webSocketError(ws) { await this.webSocketClose(ws, 1011, 'error') }
}
