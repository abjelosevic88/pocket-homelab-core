// Package remote holds the pieces of remote access that do not need Core's
// store: the relay wire format, the Noise session, the relay server itself
// and the browser shell it serves. See docs/remote.md.
package remote

import (
	"encoding/binary"
	"errors"
)

// Relay frames on the box socket: type u8 | channel u32 | payload.
const (
	RelayOpen  byte = 0x01
	RelayData  byte = 0x02
	RelayClose byte = 0x03
)

// Session frames inside the encrypted channel: type u8 | stream u32 | payload.
const (
	FrameCtl      byte = 0x01
	FrameReq      byte = 0x10
	FrameReqBody  byte = 0x11
	FrameReqEnd   byte = 0x12
	FrameRes      byte = 0x20
	FrameResBody  byte = 0x21
	FrameResEnd   byte = 0x22
	FrameResErr   byte = 0x23
	FrameCancel   byte = 0x30
	FrameWSOpen   byte = 0x40
	FrameWSOpened byte = 0x41
	FrameWSText   byte = 0x42
	FrameWSBinary byte = 0x43
	FrameWSClose  byte = 0x44
)

// MaxPlain is the largest payload one session frame carries; a Noise
// transport message is at most 65535 bytes including the 16-byte tag.
const MaxPlain = 65535 - 16 - 5

// MaxChunk is how much body goes into one frame.
const MaxChunk = 32 << 10

var ErrShortFrame = errors.New("remote: short frame")

// Encode builds one frame.
func Encode(typ byte, id uint32, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], id)
	copy(b[5:], payload)
	return b
}

// Decode splits a frame; the payload aliases b.
func Decode(b []byte) (typ byte, id uint32, payload []byte, err error) {
	if len(b) < 5 {
		return 0, 0, nil, ErrShortFrame
	}
	return b[0], binary.BigEndian.Uint32(b[1:5]), b[5:], nil
}
