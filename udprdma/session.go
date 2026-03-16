// Session holds UDPRDMA send state and implements reliable send with flow control.
// See docs/UDPRDMA.md.
package udprdma

import (
	"fmt"
	"log"
	"math"
	"net"
)

// Session is a UDPRDMA data connection session (reliable send/recv state).
type Session struct {
	writeTo  func(addr *net.UDPAddr, data []byte)
	peerAddr *net.UDPAddr

	pendingSend *PendingSend
	txBuffer    []txPacket

	txSeqNr       uint16
	txSeqNrAcked  uint16
	rxSeqExpected uint16
}

type txPacket struct {
	data []byte
	seq  uint16
}

// PendingSend holds state for a multi-packet send waiting for window ACK.
type PendingSend struct {
	Data     []byte
	Offset   int
	MaxChunk int
}

// NewSession creates a session that sends via writeTo.
func NewSession(peerAddr net.UDPAddr, writeTo func(addr *net.UDPAddr, data []byte)) *Session {
	return &Session{writeTo: writeTo, peerAddr: &peerAddr}
}

// Validates UDPRDMA DATA packet and returns payload to pass to the underlying protocol header or nil otherwise
func (s *Session) ProcessDataPacket(data []byte) (payload []byte, err error) {
	hdr, err := UnpackHeader(data)
	if err != nil || hdr.PacketType != PacketData {
		return nil, fmt.Errorf("invalid header: %v", err)
	}
	header, err := UnpackDataHeader(data[2:6])
	if err != nil {
		return nil, fmt.Errorf("invalid data header: %v", err)
	}
	payload = data[6:]
	hdrSize := int(header.HdrWordCount) * 4
	payloadSize := hdrSize + int(header.DataByteCount)
	if payloadSize > len(payload) {
		payloadSize = len(payload)
	}

	if header.Flags&uint8(DataFlagACK) != 0 {
		s.OnAck(header.SeqNrAck)
	}

	if (payloadSize == 0) && (header.Flags&uint8(DataFlagACK) != 0) {
		s.ContinuePendingSend()
		return nil, nil
	}
	if (payloadSize == 0) && (header.Flags&uint8(DataFlagACK) == 0) {
		s.OnNack(header.SeqNrAck)
		return nil, nil
	}

	expected := s.RxExpected()
	if hdr.SeqNr != expected {
		prevSeq := (expected - 1) & 0xFFF
		if hdr.SeqNr == prevSeq {
			s.SendACK(true)
			if s.HasPendingSend() {
				s.RetransmitFrom(s.FirstUnackedSeq())
			}
			return nil, nil
		}
		if hdr.SeqNr == 0 {
			log.Printf("[%s]: got unexpected sequence number 0, assuming the peer was reset", s.peerAddr)
			s.ResetTx()
			s.ResetRx()
		} else {
			log.Printf("[%s]: got unexpected sequence number %d (expected %d)", s.peerAddr, hdr.SeqNr, expected)
			s.SendACK(false)
			return nil, nil
		}
	}

	s.AdvanceRx(hdr.SeqNr)
	s.SendACK(true)

	return payload[:payloadSize], nil
}

// AdvanceRx updates expected rx sequence after accepting a packet (call with accepted seq_nr).
func (s *Session) AdvanceRx(acceptedSeqNr uint16) {
	s.rxSeqExpected = (acceptedSeqNr + 1) & 0xFFF
}

// ResetRx resets receive state (e.g. on peer reset, seq=0).
func (s *Session) ResetRx() {
	s.rxSeqExpected = 0
}

// RxExpected returns the next expected receive sequence number.
func (s *Session) RxExpected() uint16 {
	return s.rxSeqExpected
}

// FirstUnackedSeq returns the first sent sequence number not yet acked (for retransmit).
func (s *Session) FirstUnackedSeq() uint16 {
	return (s.txSeqNrAcked + 1) & 0xFFF
}

// OnAck updates send state from a received ACK and prunes txBuffer.
func (s *Session) OnAck(seqNrAck uint16) {
	s.txSeqNrAcked = seqNrAck
	newBuf := s.txBuffer[:0]
	for _, p := range s.txBuffer {
		diff := (p.seq - seqNrAck - 1) & 0xFFF
		if diff < 2048 {
			newBuf = append(newBuf, p)
		}
	}
	s.txBuffer = newBuf
}

// OnNack updates acked position and retransmits from expected seq.
func (s *Session) OnNack(seqNrAck uint16) {
	s.txSeqNrAcked = (seqNrAck - 1) & 0xFFF
	s.RetransmitFrom(seqNrAck)
}

// ResetTx resets send state (e.g. on peer reset).
func (s *Session) ResetTx() {
	s.txSeqNr = 0
	s.txSeqNrAcked = 0
	s.txBuffer = nil
	s.pendingSend = nil
}

// SendACK sends an ACK or NACK packet (no payload).
func (s *Session) SendACK(ack bool) {
	flags := uint8(0)
	if ack {
		flags = uint8(DataFlagACK)
	}
	seqAck := (s.rxSeqExpected - 1) & 0xFFF
	if !ack {
		seqAck = s.rxSeqExpected
	}
	pkt := Header{PacketType: PacketData, SeqNr: s.txSeqNr}.Pack()
	pkt = append(pkt, DataHeader{
		SeqNrAck: seqAck, Flags: flags, HdrWordCount: 0, DataByteCount: 0,
	}.Pack()...)
	s.writeTo(s.peerAddr, pkt)
}

// SendData sends a single DATA packet with full payload and FIN.
func (s *Session) SendData(payload []byte) {
	padded := (len(payload) + 3) & ^3
	buf := make([]byte, padded)
	copy(buf, payload)
	seqAck := (s.rxSeqExpected - 1) & 0xFFF
	hdr := Header{PacketType: PacketData, SeqNr: s.txSeqNr}.Pack()
	hdr = append(hdr, DataHeader{
		SeqNrAck: seqAck, Flags: uint8(DataFlagACK | DataFlagFIN),
		HdrWordCount: 0, DataByteCount: uint16(padded),
	}.Pack()...)
	packet := append(hdr, buf...)
	s.writeTo(s.peerAddr, packet)
	s.txSeqNr = (s.txSeqNr + 1) & 0xFFF
}

// SendDataPacket sends one DATA packet and buffers it for retransmit.
func (s *Session) SendDataPacket(payload []byte, fin bool, hdrSize int) {
	dataSize := len(payload) - hdrSize
	padded := (dataSize + 3) & ^3
	buf := make([]byte, len(payload[:hdrSize])+padded)
	copy(buf, payload[:hdrSize])
	copy(buf[hdrSize:], payload[hdrSize:])
	for len(buf) < hdrSize+padded {
		buf = append(buf, 0)
	}
	flags := uint8(DataFlagACK)
	if fin {
		flags |= uint8(DataFlagFIN)
	}
	seqAck := (s.rxSeqExpected - 1) & 0xFFF
	pkt := Header{PacketType: PacketData, SeqNr: s.txSeqNr}.Pack()
	pkt = append(pkt, DataHeader{
		SeqNrAck: seqAck, Flags: flags,
		HdrWordCount: uint8(hdrSize / 4), DataByteCount: uint16(padded),
	}.Pack()...)
	pkt = append(pkt, buf...)
	s.txBuffer = append(s.txBuffer, txPacket{seq: s.txSeqNr, data: pkt})
	s.writeTo(s.peerAddr, pkt)
	s.txSeqNr = (s.txSeqNr + 1) & 0xFFF
}

// RetransmitFrom retransmits buffered packets from fromSeq.
func (s *Session) RetransmitFrom(fromSeq uint16) {
	for _, p := range s.txBuffer {
		diff := (p.seq - fromSeq) & 0xFFF
		if diff < 2048 {
			s.writeTo(s.peerAddr, p.data)
		}
	}
}

// InFlight returns the number of unacknowledged packets.
func (s *Session) InFlight() int {
	return int((s.txSeqNr - s.txSeqNrAcked - 1) & 0xFFF)
}

func optimalChunkSize(totalBytes int) int {
	bestChunk := 1408
	bestPackets := int(math.Ceil(float64(totalBytes) / 1408))
	for _, maxChunk := range []int{1024, 1280, 1408} {
		packets := int(math.Ceil(float64(totalBytes) / float64(maxChunk)))
		if packets < bestPackets {
			bestPackets = packets
			bestChunk = maxChunk
		}
	}
	return bestChunk
}

// SendRawData sends data as multiple DATA packets with flow control; may set pending and return.
func (s *Session) SendRawData(data []byte) {
	s.txBuffer = nil
	s.pendingSend = nil
	maxChunk := optimalChunkSize(len(data))
	offset := 0
	for offset < len(data) {
		if s.InFlight() >= SendWindow {
			s.pendingSend = &PendingSend{Data: data, Offset: offset, MaxChunk: maxChunk}
			return
		}
		chunkSize := maxChunk
		if offset+chunkSize > len(data) {
			chunkSize = len(data) - offset
		}
		chunk := data[offset : offset+chunkSize]
		offset += chunkSize
		fin := offset >= len(data)
		s.SendDataPacket(chunk, fin, 0)
	}
}

// SendRawDataWithHeader sends header + data with header on first packet; may set pending and return.
func (s *Session) SendRawDataWithHeader(header, data []byte) {
	s.txBuffer = nil
	s.pendingSend = nil
	maxChunk := optimalChunkSize(len(data))
	firstDataMax := maxChunk
	if len(header) < MaxDataPayload {
		if MaxDataPayload-len(header) < firstDataMax {
			firstDataMax = MaxDataPayload - len(header)
		}
	}
	firstChunkSize := firstDataMax
	if firstChunkSize > len(data) {
		firstChunkSize = len(data)
	}
	firstPayload := append(append([]byte(nil), header...), data[:firstChunkSize]...)
	fin := firstChunkSize >= len(data)
	s.SendDataPacket(firstPayload, fin, len(header))
	offset := firstChunkSize
	for offset < len(data) {
		if s.InFlight() >= SendWindow {
			s.pendingSend = &PendingSend{Data: data, Offset: offset, MaxChunk: maxChunk}
			return
		}
		chunkSize := maxChunk
		if offset+chunkSize > len(data) {
			chunkSize = len(data) - offset
		}
		fin = offset+chunkSize >= len(data)
		s.SendDataPacket(data[offset:offset+chunkSize], fin, 0)
		offset += chunkSize
	}
}

// ContinuePendingSend sends more chunks when flow control allows (call after OnAck).
func (s *Session) ContinuePendingSend() {
	for s.pendingSend != nil && s.InFlight() < SendWindow {
		ps := s.pendingSend
		if ps.Offset >= len(ps.Data) {
			s.pendingSend = nil
			return
		}
		chunkSize := ps.MaxChunk
		if ps.Offset+chunkSize > len(ps.Data) {
			chunkSize = len(ps.Data) - ps.Offset
		}
		fin := ps.Offset+chunkSize >= len(ps.Data)
		chunk := ps.Data[ps.Offset : ps.Offset+chunkSize]
		ps.Offset += chunkSize
		if ps.Offset >= len(ps.Data) {
			s.pendingSend = nil
		}
		s.SendDataPacket(chunk, fin, 0)
	}
}

// HasPendingSend returns true if a multi-packet send is waiting for ACK.
func (s *Session) HasPendingSend() bool {
	return s.pendingSend != nil
}
