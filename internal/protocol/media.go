package protocol

import (
	"encoding/binary"
	"errors"
)

// MediaHeader is an application frame header intended INSIDE authenticated QUIC DATAGRAMs.
// It is not encryption and is not currently wired to a media transport.
const MediaHeaderSize = 26
const MaxMediaPayload = 1150
const MaxFragments = 4096

type MediaHeader struct {
	Channel       uint8
	Flags         uint8
	FrameID       uint32
	Fragment      uint16
	FragmentCount uint16
	TimestampUS   uint64
}

func (h MediaHeader) Marshal(payload []byte) ([]byte, error) {
	if h.Channel > 7 || h.Flags&^uint8(3) != 0 || h.FragmentCount == 0 || h.FragmentCount > MaxFragments || h.Fragment >= h.FragmentCount || len(payload) > MaxMediaPayload {
		return nil, errors.New("invalid media datagram")
	}
	b := make([]byte, MediaHeaderSize+len(payload))
	copy(b, "RDM1")
	b[4] = 1
	b[5] = h.Channel
	b[6] = h.Flags
	binary.BigEndian.PutUint32(b[8:], h.FrameID)
	binary.BigEndian.PutUint16(b[12:], h.Fragment)
	binary.BigEndian.PutUint16(b[14:], h.FragmentCount)
	binary.BigEndian.PutUint64(b[16:], h.TimestampUS)
	binary.BigEndian.PutUint16(b[24:], uint16(len(payload)))
	copy(b[26:], payload)
	return b, nil
}
func ParseMedia(b []byte) (MediaHeader, []byte, error) {
	var h MediaHeader
	if len(b) < MediaHeaderSize || len(b) > MediaHeaderSize+MaxMediaPayload || string(b[:4]) != "RDM1" || b[4] != 1 || b[7] != 0 {
		return h, nil, errors.New("invalid media header")
	}
	h = MediaHeader{Channel: b[5], Flags: b[6], FrameID: binary.BigEndian.Uint32(b[8:]), Fragment: binary.BigEndian.Uint16(b[12:]), FragmentCount: binary.BigEndian.Uint16(b[14:]), TimestampUS: binary.BigEndian.Uint64(b[16:])}
	n := int(binary.BigEndian.Uint16(b[24:]))
	if n != len(b)-MediaHeaderSize || h.Channel > 7 || h.Flags&^uint8(3) != 0 || h.FragmentCount == 0 || h.FragmentCount > MaxFragments || h.Fragment >= h.FragmentCount {
		return h, nil, errors.New("invalid media bounds")
	}
	return h, b[MediaHeaderSize:], nil
}
