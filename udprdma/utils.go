package udprdma

import "math"

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

// RetransmitFrom retransmits buffered packets from fromSeq.
func (s *Session) RetransmitFrom(fromSeq uint16) {
	for _, p := range s.txBuffer {
		diff := (p.seq - fromSeq) & 0xFFF
		if diff < 2048 {
			s.writeTo(s.peerAddr, p.data)
		}
	}
}

// ResetSession resets session state (e.g. on peer reset, seq=0).
func (s *Session) ResetSession() {
	s.txSeqNr = 0
	s.txSeqNrAcked = 0
	s.txBuffer = nil
	s.pendingSend = nil
	s.rxSeqExpected = 0
}

// InFlight returns the number of unacknowledged packets.
func (s *Session) InFlight() int {
	return int((s.txSeqNr - s.txSeqNrAcked - 1) & 0xFFF)
}

// Returns the best chunk size for given number of bytes
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
