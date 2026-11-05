// Drives assets/noise.js as the browser would, over stdin/stdout (hex lines).
import { createInterface } from 'node:readline'
import { Handshake, generateStatic, rawPublic } from '../assets/noise.js'

const hex = b => Buffer.from(b).toString('hex')
const unhex = s => new Uint8Array(Buffer.from(s.trim(), 'hex'))
const lines = createInterface({ input: process.stdin })[Symbol.asyncIterator]()
const next = async () => (await lines.next()).value

const [boxHex, host] = process.argv.slice(2)
const me = await generateStatic()
console.log('static ' + hex(await rawPublic(me)))
const hs = new Handshake(me, unhex(boxHex), 'pocket-remote/1 ' + host.toLowerCase())
console.log('m1 ' + hex(await hs.writeMessage1()))
const reply = await hs.readMessage2(unhex(await next()))
console.log('reply ' + new TextDecoder().decode(reply))
console.log('m3 ' + hex(await hs.writeMessage3(new TextEncoder().encode('{"t":"open"}'))))
for (let i = 0; i < 3; i++) console.log('c ' + hex(await hs.send.encrypt(new Uint8Array(0), new TextEncoder().encode('from js ' + i))))
for (let i = 0; i < 3; i++) console.log('p ' + new TextDecoder().decode(await hs.recv.decrypt(new Uint8Array(0), unhex(await next()))))
process.exit(0)
