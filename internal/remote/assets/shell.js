// Pocket remote access shell. It holds the encrypted session to Core (Noise
// over the relay), shows the app in a same-origin frame and carries the
// frame's requests, which the service worker hands over, through the
// session. On the launcher address it pairs devices and lists the apps.
import { Handshake, generateStatic, supported } from './noise.js'

const $ = id => document.getElementById(id)
const screen = $('screen'), frame = $('app'), banner = $('banner')
const enc = new TextEncoder(), dec = new TextDecoder()
const EMPTY = new Uint8Array(0)
const HOST = location.hostname.toLowerCase()
const F = { CTL: 0x01, REQ: 0x10, REQ_BODY: 0x11, REQ_END: 0x12, RES: 0x20, RES_BODY: 0x21, RES_END: 0x22, RES_ERR: 0x23, CANCEL: 0x30,
  WS_OPEN: 0x40, WS_OPENED: 0x41, WS_TEXT: 0x42, WS_BINARY: 0x43, WS_CLOSE: 0x44 }
const CHUNK = 32 * 1024

// ---- small helpers ----
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]))
const sleep = ms => new Promise(r => setTimeout(r, ms))
function b64u(buf) {
  const b = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  let s = ''
  for (const x of b) s += String.fromCharCode(x)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}
function unb64u(s) {
  const t = atob(s.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((s.length + 3) % 4))
  return Uint8Array.from(t, c => c.charCodeAt(0))
}
const same = (a, b) => a && b && a.length === b.length && a.every((x, i) => x === b[i])
function frameBytes(type, id, payload) {
  const p = payload || EMPTY
  const b = new Uint8Array(5 + p.length)
  b[0] = type
  new DataView(b.buffer).setUint32(1, id, false)
  b.set(p, 5)
  return b
}
const json = v => enc.encode(JSON.stringify(v))
function concat(parts) {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0))
  let o = 0
  for (const p of parts) { out.set(p, o); o += p.length }
  return out
}

function deviceName() {
  const ua = navigator.userAgent
  const p = /iPhone/.test(ua) ? 'iPhone' : /iPad/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1) ? 'iPad'
    : /Android/.test(ua) ? 'Android' : /Mac OS X/.test(ua) ? 'Mac' : /Windows/.test(ua) ? 'Windows PC' : /Linux/.test(ua) ? 'Linux' : 'Device'
  const b = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : ''
  return b ? `${p} · ${b}` : p
}

// ---- storage: the device key lives in IndexedDB as a non-extractable CryptoKey ----
function db() {
  return new Promise((res, rej) => {
    const r = indexedDB.open('pocket-remote', 1)
    r.onupgradeneeded = () => r.result.createObjectStore('kv')
    r.onsuccess = () => res(r.result)
    r.onerror = () => rej(r.error)
  })
}
async function getRaw(k) {
  const d = await db()
  return new Promise((res, rej) => {
    const r = d.transaction('kv').objectStore('kv').get(k)
    r.onsuccess = () => res(r.result)
    r.onerror = () => rej(r.error)
  })
}
async function putRaw(k, v) {
  const d = await db()
  return new Promise((res, rej) => {
    const t = d.transaction('kv', 'readwrite')
    if (v === undefined) t.objectStore('kv').delete(k); else t.objectStore('kv').put(v, k)
    t.oncomplete = res
    t.onerror = () => rej(t.error)
    t.onabort = () => rej(t.error || new Error('storage aborted'))
  })
}

// WebKit (Safari, every iPhone browser) silently drops X25519 CryptoKeys from
// IndexedDB, though it keeps AES keys. There the device key is kept wrapped by
// a stored AES key and unwrapped into a non-extractable key on load. Chromium
// and Firefox keep the X25519 key itself.
let keepsX25519 = null
async function x25519Persists() {
  if (keepsX25519 !== null) return keepsX25519
  try {
    const probe = await crypto.subtle.generateKey({ name: 'X25519' }, false, ['deriveBits'])
    await putRaw('probe', { k: probe.privateKey })
    const back = await getRaw('probe')
    keepsX25519 = !!(back && back.k && back.k.type === 'private')
  } catch { keepsX25519 = false }
  await putRaw('probe', undefined).catch(() => {})
  return keepsX25519
}

async function newDeviceKey() {
  if (await x25519Persists()) return { key: await generateStatic() }
  const pair = await crypto.subtle.generateKey({ name: 'X25519' }, true, ['deriveBits'])
  const wrapper = await crypto.subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, ['wrapKey', 'unwrapKey'])
  const iv = crypto.getRandomValues(new Uint8Array(12))
  const wrapped = await crypto.subtle.wrapKey('pkcs8', pair.privateKey, wrapper, { name: 'AES-GCM', iv })
  const pub = new Uint8Array(await crypto.subtle.exportKey('raw', pair.publicKey))
  return { wrapped: { wrapped, iv, wrapper, pub }, key: await unwrapDeviceKey({ wrapped, iv, wrapper, pub }) }
}

async function unwrapDeviceKey(w) {
  const privateKey = await crypto.subtle.unwrapKey('pkcs8', w.wrapped, w.wrapper, { name: 'AES-GCM', iv: w.iv }, { name: 'X25519' }, false, ['deriveBits'])
  const publicKey = await crypto.subtle.importKey('raw', w.pub, { name: 'X25519' }, true, [])
  return { privateKey, publicKey }
}

async function load() {
  const st = (await getRaw('state')) || {}
  if (st.wrapped && !st.key) st.key = await unwrapDeviceKey(st.wrapped)
  return st
}
async function save(state) {
  // A wrapped key is stored wrapped only: a record holding an X25519 key would vanish in WebKit.
  const rec = state.wrapped ? { ...state, key: undefined } : state
  await putRaw('state', rec)
}

// ---- screens ----
function show(html, top) {
  screen.hidden = false
  screen.className = 'screen' + (top ? ' top' : '')
  screen.innerHTML = html
}
function message(title, text, actions = '') {
  show(`<div class="card"><h1>${esc(title)}</h1><p class="muted">${esc(text)}</p>${actions}</div>`)
}
function setBanner(text) {
  if (!text) { banner.hidden = true; return }
  banner.innerHTML = `<div class="spinner"></div><span>${esc(text)}</span>`
  banner.hidden = false
}

// ---- the encrypted session ----
class Session {
  constructor() {
    this.streams = new Map()
    this.nextId = 1
    this.chain = Promise.resolve()
    this.queue = []
    this.wake = null
    this.dead = false
    this.ticks = 0
    this.ctlWaiters = []
    this.ctlQueue = []
    this.closed = new Promise(r => { this.onClosed = r })
  }

  open() {
    const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = this.ws = new WebSocket(`${scheme}//${location.host}/__pocket/tunnel`)
    ws.binaryType = 'arraybuffer'
    ws.onmessage = e => {
      if (typeof e.data === 'string') return // the relay's keepalive answer
      this.queue.push(new Uint8Array(e.data))
      this.wake?.()
    }
    return new Promise((res, rej) => {
      ws.onopen = () => { ws.onclose = e => this.die(e.reason || 'connection closed'); res() }
      ws.onclose = () => rej(new Error('The relay could not be reached.'))
    })
  }

  async next() {
    while (!this.queue.length) {
      if (this.dead) throw new Error(this.why || 'connection closed')
      await new Promise(r => { this.wake = r })
    }
    return this.queue.shift()
  }

  die(why) {
    if (this.dead) return
    this.dead = true
    this.why = why
    this.wake?.()
    for (const w of this.ctlWaiters) w.reject(new Error(why))
    this.ctlWaiters = []
    clearInterval(this.keepalive)
    for (const st of this.streams.values()) {
      if (st.kind === 'http') st.sink({ t: 'error', message: 'connection lost' })
      else st.h.close(1006, 'connection lost')
    }
    this.streams.clear()
    try { this.ws.close() } catch {}
    this.onClosed(why)
  }

  // connect runs the handshake and returns Core's first control message.
  async connect(state, hello) {
    await this.open()
    const hs = new Handshake(state.key, state.box, 'pocket-remote/1 ' + HOST)
    this.ws.send(await hs.writeMessage1())
    const m2 = await this.next()
    try { await hs.readMessage2(m2) } catch { const e = new Error('box-key'); e.boxKey = true; throw e }
    this.ws.send(await hs.writeMessage3(json(hello)))
    this.tx = hs.send
    this.rx = hs.recv
    this.reader()
    this.keepalive = setInterval(() => {
      try { this.ws.send('ping') } catch {}
      if (++this.ticks % 9 === 0) this.ctl({ t: 'ping' })
    }, 25000)
    return this.nextCtl()
  }

  async reader() {
    try {
      for (;;) {
        const m = await this.next()
        this.onFrame(await this.rx.decrypt(EMPTY, m))
      }
    } catch (e) {
      this.die(e.message)
    }
  }

  nextCtl() {
    if (this.ctlQueue.length) return Promise.resolve(this.ctlQueue.shift())
    return new Promise((resolve, reject) => this.ctlWaiters.push({ resolve, reject }))
  }

  // send encrypts in call order; nonces must reach Core in that order.
  send(type, id, payload) {
    const plain = frameBytes(type, id, payload)
    this.chain = this.chain.then(async () => {
      if (this.dead) return
      this.ws.send(await this.tx.encrypt(EMPTY, plain))
      while (this.ws.bufferedAmount > (4 << 20) && !this.dead) await sleep(20)
    })
    return this.chain
  }

  ctl(v) { return this.send(F.CTL, 0, json(v)) }

  onFrame(b) {
    const type = b[0], id = new DataView(b.buffer, b.byteOffset).getUint32(1, false), p = b.subarray(5)
    if (type === F.CTL) {
      const m = JSON.parse(dec.decode(p))
      if (this.onControl && this.onControl(m)) return
      const w = this.ctlWaiters.shift()
      if (w) w.resolve(m); else this.ctlQueue.push(m)
      return
    }
    const st = this.streams.get(id)
    if (!st) return
    if (st.kind === 'http') {
      if (type === F.RES) {
        const h = JSON.parse(dec.decode(p))
        st.sink({ t: 'head', status: h.s, headers: h.h })
      } else if (type === F.RES_BODY) {
        const buf = p.slice().buffer
        st.sink({ t: 'chunk', data: buf }, [buf])
      } else if (type === F.RES_END) {
        this.streams.delete(id)
        st.sink({ t: 'end' })
      } else if (type === F.RES_ERR) {
        this.streams.delete(id)
        st.sink({ t: 'error', message: dec.decode(p) })
      }
      return
    }
    if (type === F.WS_OPENED) st.h.open(JSON.parse(dec.decode(p)).p)
    else if (type === F.WS_TEXT || type === F.WS_BINARY) {
      st.parts.push(p.slice(1))
      if (p[0] === 1) {
        const all = st.parts.length === 1 ? st.parts[0] : concat(st.parts)
        st.parts = []
        if (type === F.WS_TEXT) st.h.text(dec.decode(all)); else st.h.binary(all)
      }
    } else if (type === F.WS_CLOSE) {
      const c = JSON.parse(dec.decode(p))
      this.streams.delete(id)
      st.h.close(c.c, c.r)
    }
  }

  // request runs one HTTP request; sink gets head/chunk/end/error messages.
  request(method, url, headers, body, sink) {
    const id = this.nextId++
    this.streams.set(id, { kind: 'http', sink })
    const has = !!(body && body.byteLength)
    this.send(F.REQ, id, json({ m: method, u: url, h: has ? headers.concat([["content-length", String(body.byteLength)]]) : headers, b: has }))
    if (has) {
      const b = new Uint8Array(body)
      for (let o = 0; o < b.length; o += CHUNK) this.send(F.REQ_BODY, id, b.subarray(o, o + CHUNK))
      this.send(F.REQ_END, id)
    }
    return id
  }

  cancel(id) {
    if (this.streams.delete(id)) this.send(F.CANCEL, id)
  }

  // fetchRaw and fetchJSON are the launcher's own calls to Core.
  fetchRaw(method, url, body) {
    return new Promise((resolve, reject) => {
      let head = null
      const parts = []
      this.request(method, url, [['content-type', 'application/json']], body ? json(body) : null, m => {
        if (m.t === 'head') head = m
        else if (m.t === 'chunk') parts.push(new Uint8Array(m.data))
        else if (m.t === 'end') resolve({ status: head.status, headers: new Headers(head.headers), body: concat(parts) })
        else if (m.t === 'error') reject(new Error(m.message))
      })
    })
  }
  async fetchJSON(method, url, body) {
    const r = await this.fetchRaw(method, url, body)
    const v = JSON.parse(dec.decode(r.body) || '{}')
    if (r.status >= 400) throw new Error(v.error || `HTTP ${r.status}`)
    return v
  }

  openSocket(path, protocols, h) {
    const id = this.nextId++
    this.streams.set(id, { kind: 'ws', h, parts: [] })
    this.send(F.WS_OPEN, id, json({ u: path, p: protocols }))
    return {
      send: async (text, u8) => {
        const type = text ? F.WS_TEXT : F.WS_BINARY
        let o = 0
        do {
          const part = u8.subarray(o, o + CHUNK)
          o += CHUNK
          const p = new Uint8Array(part.length + 1)
          p[0] = o >= u8.length ? 1 : 0
          p.set(part, 1)
          await this.send(type, id, p)
        } while (o < u8.length)
      },
      close: (code, reason) => {
        if (!this.streams.delete(id)) return
        this.send(F.WS_CLOSE, id, json({ c: code, r: reason }))
        setTimeout(() => h.close(code, reason), 0)
      },
    }
  }
}

// ---- requests from the service worker ----
let current = null
let pending = []

function fromWorker(msg, port) {
  const s = current
  if (!s) { pending.push([msg, port]); return }
  const id = s.request(msg.method, msg.url, msg.headers, msg.body, (m, transfer) => port.postMessage(m, transfer || []))
  port.onmessage = e => { if (e.data?.t === 'cancel') s.cancel(id) }
}
function flush() {
  const list = pending
  pending = []
  for (const [m, p] of list) fromWorker(m, p)
}

// window.__pocketShell is what inject.js (inside the app frame) talks to.
const sockets = new Set()
window.__pocketShell = {
  openSocket(path, protocols, h) {
    if (!current) {
      setTimeout(() => h.close(1006, 'not connected'), 0)
      return { send() {}, close() {} }
    }
    let handle = null
    handle = current.openSocket(path, protocols, {
      open: p => h.open(p), text: t => h.text(t), binary: b => h.binary(b),
      close: (c, r) => { sockets.delete(handle); h.close(c, r) },
    })
    handle.notify = (c, r) => h.close(c, r)
    sockets.add(handle)
    return handle
  },
  navigated(path, title) { syncFrom(path, title) },
  themeColor(c) { if (c) document.querySelector('meta[name="theme-color"]')?.setAttribute('content', c) },
}

function syncFrom(path, title) {
  if (path && !path.startsWith('/__pocket/')) history.replaceState(null, '', path)
  if (title) document.title = title
}
frame.addEventListener('load', () => {
  try {
    const l = frame.contentWindow.location
    syncFrom(l.pathname + l.search + l.hash, frame.contentDocument?.title)
  } catch {}
})

// ---- passkey (Face ID / Touch ID / device PIN) ----
function passkeyStep(s, c) {
  return new Promise(resolve => {
    const setup = c.t === 'passkey-setup'
    const render = (err = '') => {
      show(`<div class="card"><svg class="lock" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="4.5" y="10.5" width="15" height="10" rx="2.5"/><path d="M8 10.5V7.5a4 4 0 0 1 8 0v3"/><circle cx="12" cy="15.5" r="1.3" fill="currentColor"/></svg><h1>${esc(c.app)}</h1>
        <p class="muted">${setup ? 'This app needs a passkey on this device. Create one with Face ID, Touch ID or your device PIN.' : 'Unlock with Face ID, Touch ID or your device PIN.'}</p>
        ${err ? `<p class="err">${esc(err)}</p>` : ''}
        <button class="btn" id="pk">${setup ? 'Create passkey' : 'Unlock'}</button><br><button class="btn ghost" id="pkc">Cancel</button></div>`)
      $('pkc').onclick = () => { s.ctl({ t: 'passkey-cancel' }); resolve() }
      $('pk').onclick = go
    }
    const go = async () => {
      try {
        if (setup) {
          const cred = await navigator.credentials.create({ publicKey: {
            challenge: unb64u(c.challenge), rp: { id: c.rp, name: c.app },
            user: { id: unb64u(c.user.id), name: c.user.name, displayName: c.user.display },
            pubKeyCredParams: [{ type: 'public-key', alg: -7 }, { type: 'public-key', alg: -8 }, { type: 'public-key', alg: -257 }],
            authenticatorSelection: { userVerification: 'required', residentKey: 'preferred' }, attestation: 'none', timeout: 120000 } })
          const r = cred.response
          const pub = r.getPublicKey?.()
          if (!pub) throw new Error('This browser cannot hand over the passkey. Use Safari, Chrome or Firefox.')
          await s.ctl({ t: 'passkey-setup', id: b64u(cred.rawId), clientData: b64u(r.clientDataJSON), authData: b64u(r.getAuthenticatorData()),
            publicKey: b64u(pub), alg: r.getPublicKeyAlgorithm() })
        } else {
          const cred = await navigator.credentials.get({ publicKey: {
            challenge: unb64u(c.challenge), rpId: c.rp, allowCredentials: [{ type: 'public-key', id: unb64u(c.id) }],
            userVerification: 'required', timeout: 120000 } })
          const r = cred.response
          await s.ctl({ t: 'passkey', id: b64u(cred.rawId), clientData: b64u(r.clientDataJSON), authData: b64u(r.authenticatorData), signature: b64u(r.signature) })
        }
        show('<div class="card"><div class="spinner"></div><p class="muted">Checking…</p></div>')
        resolve()
      } catch (e) {
        render(e.name === 'NotAllowedError' ? 'Not unlocked. Try again.' : e.message)
      }
    }
    render()
  })
}

// ---- launcher ----
async function launcher(s) {
  let data
  try { data = await s.fetchJSON('GET', '/__pocket/api/apps') } catch (e) { message('Could not load your apps', e.message); return }
  const family = data.family || 'Pocket'
  document.title = `${family} apps`
  const tiles = data.apps.map(a => `<button class="tile" data-slug="${esc(a.slug)}" ${a.allowed ? '' : 'disabled'}>
      <span class="ph" data-icon="${esc(a.slug)}">${esc((a.name.replace(/^\S+\s+/, '') || a.name)[0] || '?')}</span>
      <span>${esc(a.name)}</span>${a.allowed ? '' : '<small>Off on this device</small>'}</button>`).join('')
  show(`<div class="launcher"><header><h1>${esc(family)} apps</h1><span class="muted">${esc(data.device)}</span></header>
    ${data.apps.length ? `<div class="grid">${tiles}</div>` : '<p class="muted">No apps are reachable remotely yet. An admin can switch them on in Core → Remote access.</p>'}
    <p class="note muted">Signed in as ${esc(data.user)} over an end-to-end encrypted connection. Manage this device in Core → Remote access.</p></div>`, true)
  for (const el of screen.querySelectorAll('[data-icon]')) {
    s.fetchRaw('GET', '/__pocket/api/icon/' + encodeURIComponent(el.dataset.icon)).then(r => {
      if (r.status !== 200) return
      const img = new Image()
      img.alt = ''
      img.src = URL.createObjectURL(new Blob([r.body], { type: r.headers.get('content-type') || 'image/svg+xml' }))
      el.replaceWith(img)
    }).catch(() => {})
  }
  for (const el of screen.querySelectorAll('.tile')) el.onclick = () => openApp(s, el.dataset.slug, null)
}

async function openApp(s, slug, target) {
  show('<div class="card"><div class="spinner"></div><p class="muted">Opening…</p></div>')
  try {
    const r = await s.fetchJSON('POST', '/__pocket/api/open', { app: slug })
    const u = new URL(r.url)
    if (target) {
      const t = new URL(target)
      if (t.host === u.host) { t.hash = u.hash; location.replace(t.href); return }
    }
    location.assign(u.href)
  } catch (e) {
    message('Cannot open this app', e.message, '<a class="btn ghost" href="/">Back to your apps</a>')
  }
}

async function handoff(s, target) {
  let host
  try { host = new URL(target).host } catch { return launcher(s) }
  const data = await s.fetchJSON('GET', '/__pocket/api/apps').catch(() => ({ apps: [] }))
  const app = data.apps.find(a => new URL(a.url).host === host)
  if (!app) return message('Not reachable remotely', 'That app is not switched on for remote access.', '<a class="btn ghost" href="/">Your apps</a>')
  if (!app.allowed) return message('Off on this device', 'This app is switched off for this device. Switch it on in Core → Remote access.', '<a class="btn ghost" href="/">Your apps</a>')
  return openApp(s, app.slug, target)
}

// ---- main ----
async function portalURL() {
  try { return (await (await fetch('/__pocket/config.json', { cache: 'no-store' })).json()).portal || '' } catch { return '' }
}

// notPaired sends an app address to the launcher, which hands this device a
// key for it and comes straight back.
async function notPaired() {
  const portal = await portalURL()
  if (portal && new URL(portal).host !== location.host) {
    location.replace(portal.replace(/\/$/, '') + '/#open=' + encodeURIComponent(location.href))
    return
  }
  message('Pair this device', 'At home, open Core → Remote access → Pair a device, then scan the code with this device or open its link here.')
}

function fatal(c, state) {
  const back = '<a class="btn ghost" href="">Try again</a>'
  frame.hidden = true
  switch (c.code) {
    case 'not_paired':
      state.paired = false
      save(state).then(notPaired)
      return
    case 'bad_code': return message('Pairing link used up', c.message)
    case 'passkey': return message('Not unlocked', c.message, back)
    case 'revoked': return message('Access switched off', c.message, back)
    default: return message('Cannot open this', c.message || 'Core refused the connection.', back)
  }
}

async function main() {
  const frag = new URLSearchParams(location.hash.slice(1))
  let pair = null
  let openTarget = null
  if (frag.get('pair') && frag.get('box')) pair = { code: frag.get('pair'), box: unb64u(frag.get('box')) }
  if (frag.get('open')) openTarget = frag.get('open')
  if (pair || openTarget) history.replaceState(null, '', location.pathname + location.search)

  if (!window.isSecureContext || !('serviceWorker' in navigator) || !('indexedDB' in window)) {
    return message('Not supported here', 'Remote access needs a current browser over HTTPS, outside private browsing.')
  }
  if (!(await supported())) {
    return message('Browser too old', 'Remote access needs iOS 17, Safari 17, Chrome 133 or Firefox 130 or newer.')
  }
  let state = await load().catch(() => ({}))
  if (pair && (!same(state.box, pair.box) || !state.key || !state.paired)) {
    const keep = state.key && same(state.box, pair.box)
    const fresh = keep ? { key: state.key, wrapped: state.wrapped } : await newDeviceKey()
    state = { box: pair.box, key: fresh.key, wrapped: fresh.wrapped, paired: false }
    await save(state)
  } else if (pair) {
    pair = null // already paired here; keep the existing key
  }
  if (!state.key || !state.box || (!state.paired && !pair)) return notPaired()

  navigator.serviceWorker.addEventListener('message', e => { if (e.data?.type === 'pocket-fetch') fromWorker(e.data, e.ports[0]) })
  navigator.serviceWorker.startMessages()
  await navigator.serviceWorker.register('/__pocket/sw.js', { scope: '/' })
  await navigator.serviceWorker.ready

  let hello = pair ? { t: 'pair', code: pair.code, name: deviceName() } : { t: 'open' }
  let shown = false
  let delay = 1000
  for (;;) {
    const s = new Session()
    let reachedCore = false
    try {
      let c = await s.connect(state, hello)
      reachedCore = true
      while (c.t === 'passkey' || c.t === 'passkey-setup') {
        await passkeyStep(s, c)
        c = await s.nextCtl()
      }
      if (c.t === 'error') { s.die('refused'); return fatal(c, state) }
      if (c.t !== 'ready') throw new Error('Unexpected answer from Core.')
      if (hello.t === 'pair') { state.paired = true; await save(state); hello = { t: 'open' } }
      s.onControl = m => { if (m.t === 'error') { s.die('refused'); fatal(m, state); return true } return false }
      current = s
      delay = 1000
      setBanner('')
      if (!shown) {
        shown = true
        if (c.kind === 'portal') {
          if (openTarget) await handoff(s, openTarget); else await launcher(s)
        } else {
          document.title = c.app?.name || document.title
          screen.hidden = true
          frame.hidden = false
          frame.src = location.pathname.startsWith('/__pocket/') ? '/' : location.pathname + location.search + location.hash
        }
      } else if (c.kind === 'app') {
        screen.hidden = true
      }
      flush()
      await s.closed
      if (s.why === 'refused') return
    } catch (e) {
      s.die(e.message)
      if (e.boxKey) {
        state.paired = false
        await save(state)
        return message('Pair again', 'Core\'s key changed (remote access was reset). Pair this device again from Core → Remote access.')
      }
      if (hello.t === 'pair' && reachedCore) return message('Pairing failed', e.message)
    }
    current = null
    for (const h of sockets) h.notify(1006, 'connection lost')
    sockets.clear()
    if (shown) setBanner('Reconnecting…')
    else show('<div class="card"><div class="spinner"></div><p class="muted">Cannot reach your box. Retrying…</p></div>')
    await Promise.race([sleep(delay), new Promise(r => document.addEventListener('visibilitychange', () => { if (!document.hidden) r() }, { once: true }))])
    delay = Math.min(delay * 2, 30000)
  }
}

main().catch(e => message('Something went wrong', e.message))
