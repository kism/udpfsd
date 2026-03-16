package udpfsd

import (
	"errors"
	"log"
	"net"
	"time"

	"github.com/pcm720/udpfsd/udpfs"
	"github.com/pcm720/udpfsd/udprdma"
)

func (s *Server) dataHandler() {
	s.dataConn.SetReadBuffer(2048)
	buf := make([]byte, 2048)
	for {
		n, addr, err := s.dataConn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				log.Printf("udpfsd/data: connection has been closed")
				s.wg.Done()
				return
			}
			if s.verbose {
				log.Printf("udpfsd/data: read error: %v", err)
			}
			continue
		}
		if n < 6 {
			continue
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		s.handleData(pkt, addr)
	}
}

func (s *Server) handleData(data []byte, addr *net.UDPAddr) {
	// Get connection handle
	s.Lock()
	c, ok := s.cMap[addr.String()]
	if !ok {
		log.Printf("[%s]: creating new connection", addr)
		c = &peer{
			udpfs.NewConnection(
				udprdma.NewSession(*addr, func(addr *net.UDPAddr, data []byte) {
					s.dataConn.WriteToUDP(data, addr)
				}),
				s.fs,
				s.verbose,
			),
			time.Now(),
		}
		s.cMap[addr.String()] = c
	} else {
		// Update last seen time
		c.lastSeen = time.Now()
	}
	s.Unlock()

	payload, err := c.GetUDPRDMASession().ProcessDataPacket(data)
	if err != nil {
		if s.verbose {
			log.Printf("udpfsd/data: [%s]: %v", addr, err)
		}
		return
	}

	if payload != nil {
		c.HandlePayload(addr, payload)
	}
}
