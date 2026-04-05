package udprdma

type metricContainer struct {
	packetsTx        uint64
	packetsRx        uint64
	retransmits      uint64
	nacks            uint64
	unexpectedSeqNrs uint64
	peerNACKs        uint64
	peerResets       uint64
}

type SessionStats struct {
	TotalPacketsTx       uint64
	TotalPacketsRx       uint64
	Retransmits          uint64
	NACKCount            uint64
	UnexpectedSeqNrCount uint64
	PeerNACKCount        uint64
	PeerResetCount       uint64
}

func (s *Session) GetMetrics() SessionStats {
	s.Lock()
	defer s.Unlock()

	return SessionStats{
		TotalPacketsTx:       s.packetsTx,
		TotalPacketsRx:       s.packetsRx,
		Retransmits:          s.retransmits,
		NACKCount:            s.nacks,
		UnexpectedSeqNrCount: s.unexpectedSeqNrs,
		PeerNACKCount:        s.peerNACKs,
		PeerResetCount:       s.peerResets,
	}
}
