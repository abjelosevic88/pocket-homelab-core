// Runs first in every app page shown through Pocket remote access. It sends
// same-host WebSockets through the shell's encrypted session, keeps the app
// from replacing the remote-access service worker, and tells the shell where
// the app is (address, title, colour).
(function () {
  'use strict'
  var P
  try { P = window.parent && window.parent !== window && window.parent.__pocketShell } catch (e) { P = null }
  if (!P) return

  // 1. No service worker of the app's own: ours must stay in charge.
  var sw = navigator.serviceWorker
  if (sw) {
    try {
      Object.defineProperty(sw, 'register', { value: function () { return Promise.reject(new DOMException('Service workers are off over Pocket remote access', 'SecurityError')) } })
      Object.defineProperty(sw, 'getRegistrations', { value: function () { return Promise.resolve([]) } })
      Object.defineProperty(sw, 'getRegistration', { value: function () { return Promise.resolve(undefined) } })
    } catch (e) {}
  }

  // 2. WebSockets to this address go through the session.
  var Native = window.WebSocket
  function copyBytes(u8) { var b = new ArrayBuffer(u8.byteLength); new Uint8Array(b).set(u8); return b }

  function RemoteSocket(url, protocols) {
    var self = this
    var et = document.createDocumentFragment() // a same-realm EventTarget
    ;['addEventListener', 'removeEventListener', 'dispatchEvent'].forEach(function (m) { self[m] = et[m].bind(et) })
    this.url = url.href
    this.readyState = 0
    this.protocol = ''
    this.extensions = ''
    this.bufferedAmount = 0
    this.binaryType = 'blob'
    this.onopen = this.onmessage = this.onclose = this.onerror = null
    var queue = Promise.resolve()
    function fire(type, init) {
      var ev = type === 'message' ? new MessageEvent('message', init) : type === 'close' ? new CloseEvent('close', init) : new Event(type)
      var h = self['on' + type]
      self.dispatchEvent(ev)
      if (typeof h === 'function') h.call(self, ev)
    }
    this._h = P.openSocket(url.pathname + url.search, protocols ? [].concat(protocols) : [], {
      open: function (proto) { self.readyState = 1; self.protocol = proto || ''; fire('open') },
      text: function (s) { if (self.readyState === 1) fire('message', { data: String(s), origin: location.origin }) },
      binary: function (u8) {
        if (self.readyState !== 1) return
        var buf = copyBytes(u8)
        fire('message', { data: self.binaryType === 'arraybuffer' ? buf : new Blob([buf]), origin: location.origin })
      },
      close: function (code, reason) {
        if (self.readyState === 3) return
        var clean = code === 1000
        self.readyState = 3
        if (!clean && code !== 1001) fire('error')
        fire('close', { code: code || 1006, reason: reason || '', wasClean: clean })
      },
    })
    this.send = function (data) {
      if (self.readyState === 0) throw new DOMException('Still connecting', 'InvalidStateError')
      if (self.readyState !== 1) return
      queue = queue.then(function () {
        if (typeof data === 'string') return self._h.send(true, new TextEncoder().encode(data))
        if (data instanceof Blob) return data.arrayBuffer().then(function (b) { self._h.send(false, new Uint8Array(b)) })
        if (ArrayBuffer.isView(data)) return self._h.send(false, new Uint8Array(data.buffer, data.byteOffset, data.byteLength))
        return self._h.send(false, new Uint8Array(data))
      })
    }
    this.close = function (code, reason) {
      if (self.readyState >= 2) return
      self.readyState = 2
      self._h.close(code || 1000, reason || '')
    }
  }
  RemoteSocket.prototype = Object.create(EventTarget.prototype)

  function PocketWebSocket(url, protocols) {
    var u = new URL(url, location.href)
    if (u.host !== location.host || !/^wss?:$/.test(u.protocol)) return new Native(url, protocols)
    return new RemoteSocket(u, protocols)
  }
  ;['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED'].forEach(function (k, i) {
    PocketWebSocket[k] = i
    RemoteSocket.prototype[k] = i
  })
  PocketWebSocket.prototype = Native.prototype
  window.WebSocket = PocketWebSocket

  // 3. Keep the shell's address bar, title and colour in step with the app.
  function report() { try { P.navigated(location.pathname + location.search + location.hash, document.title) } catch (e) {} }
  ;['pushState', 'replaceState'].forEach(function (m) {
    var orig = history[m]
    history[m] = function () { var r = orig.apply(this, arguments); report(); return r }
  })
  addEventListener('popstate', report)
  addEventListener('hashchange', report)
  document.addEventListener('DOMContentLoaded', function () {
    report()
    var t = document.querySelector('title')
    if (t) new MutationObserver(report).observe(t, { childList: true, characterData: true, subtree: true })
    var meta = document.querySelector('meta[name="theme-color"]')
    if (meta) try { P.themeColor(meta.getAttribute('content')) } catch (e) {}
  })
})()
