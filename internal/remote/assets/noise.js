// Noise_XK_25519_AESGCM_SHA256, initiator side, on WebCrypto only.
// Spec: https://noiseprotocol.org/noise.html. Core runs the responder with
// github.com/flynn/noise; internal/remote/noise_interop_test.go checks they agree.

const subtle = crypto.subtle
const NAME = 'Noise_XK_25519_AESGCM_SHA256'
const enc = new TextEncoder()

const concat = (...parts) => {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0))
  let o = 0
  for (const p of parts) { out.set(p, o); o += p.length }
  return out
}

async function sha256(data) { return new Uint8Array(await subtle.digest('SHA-256', data)) }

async function hmac(key, data) {
  const k = await subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
  return new Uint8Array(await subtle.sign('HMAC', k, data))
}

async function hkdf(ck, ikm) {
  const temp = await hmac(ck, ikm)
  const o1 = await hmac(temp, new Uint8Array([1]))
  const o2 = await hmac(temp, concat(o1, new Uint8Array([2])))
  return [o1, o2]
}

function nonce(n) {
  const iv = new Uint8Array(12)
  new DataView(iv.buffer).setBigUint64(4, BigInt(n), false)
  return iv
}

export class CipherState {
  constructor(raw) { this.raw = raw; this.n = 0; this.key = null }
  async ready() {
    if (!this.key) this.key = await subtle.importKey('raw', this.raw, 'AES-GCM', false, ['encrypt', 'decrypt'])
    return this.key
  }
  // The nonce is taken synchronously, so callers that start several
  // encryptions must also send the results in call order.
  async encrypt(ad, plain) {
    const iv = nonce(this.n++)
    const key = await this.ready()
    return new Uint8Array(await subtle.encrypt({ name: 'AES-GCM', iv, additionalData: ad }, key, plain))
  }
  async decrypt(ad, data) {
    const iv = nonce(this.n++)
    const key = await this.ready()
    return new Uint8Array(await subtle.decrypt({ name: 'AES-GCM', iv, additionalData: ad }, key, data))
  }
}

const EMPTY = new Uint8Array(0)

class Symmetric {
  async init(prologue) {
    const name = enc.encode(NAME)
    this.h = name.length <= 32 ? concat(name, new Uint8Array(32 - name.length)) : await sha256(name)
    this.ck = this.h
    this.k = null
    await this.mixHash(prologue)
  }
  async mixHash(data) { this.h = await sha256(concat(this.h, data)) }
  async mixKey(ikm) {
    const [ck, k] = await hkdf(this.ck, ikm)
    this.ck = ck
    this.k = new CipherState(k)
  }
  async encryptAndHash(plain) {
    const out = this.k ? await this.k.encrypt(this.h, plain) : plain
    await this.mixHash(out)
    return out
  }
  async decryptAndHash(data) {
    const out = this.k ? await this.k.decrypt(this.h, data) : data
    await this.mixHash(data)
    return out
  }
  async split() {
    const [a, b] = await hkdf(this.ck, EMPTY)
    return [new CipherState(a), new CipherState(b)]
  }
}

async function dh(priv, pubRaw) {
  const pub = await subtle.importKey('raw', pubRaw, { name: 'X25519' }, true, [])
  return new Uint8Array(await subtle.deriveBits({ name: 'X25519', public: pub }, priv, 256))
}

export async function rawPublic(pair) {
  return new Uint8Array(await subtle.exportKey('raw', pair.publicKey))
}

// generateStatic makes the device key. Its private half cannot be exported:
// a script can use it while the page is open but never copy it elsewhere.
export async function generateStatic() {
  return subtle.generateKey({ name: 'X25519' }, false, ['deriveBits'])
}

export async function supported() {
  try {
    const k = await subtle.generateKey({ name: 'X25519' }, false, ['deriveBits'])
    return !!k.privateKey
  } catch { return false }
}

// Handshake runs `<- s ... -> e, es / <- e, ee / -> s, se`.
export class Handshake {
  constructor(staticPair, remoteStatic, prologue) {
    this.s = staticPair
    this.rs = remoteStatic
    this.prologue = typeof prologue === 'string' ? enc.encode(prologue) : prologue
  }
  async writeMessage1(payload = EMPTY) {
    this.ss = new Symmetric()
    await this.ss.init(this.prologue)
    await this.ss.mixHash(this.rs)
    this.e = await subtle.generateKey({ name: 'X25519' }, false, ['deriveBits'])
    const epub = await rawPublic(this.e)
    await this.ss.mixHash(epub)
    await this.ss.mixKey(await dh(this.e.privateKey, this.rs))
    return concat(epub, await this.ss.encryptAndHash(payload))
  }
  async readMessage2(msg) {
    if (msg.length < 32 + 16) throw new Error('noise: short message')
    this.re = msg.slice(0, 32)
    await this.ss.mixHash(this.re)
    await this.ss.mixKey(await dh(this.e.privateKey, this.re))
    return this.ss.decryptAndHash(msg.slice(32))
  }
  async writeMessage3(payload = EMPTY) {
    const spub = await rawPublic(this.s)
    const encS = await this.ss.encryptAndHash(spub)
    await this.ss.mixKey(await dh(this.s.privateKey, this.re))
    const body = await this.ss.encryptAndHash(payload)
    const [send, recv] = await this.ss.split()
    this.send = send
    this.recv = recv
    return concat(encS, body)
  }
}
