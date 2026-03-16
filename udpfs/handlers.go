package udpfs

import (
	"bytes"
	"encoding/binary"
	"log"
	"net"
)

// HandlePayload parses the message type and dispatches to the appropriate handler.
// Call this when a UDPFS data payload has been received and session state has been updated.
func (c *Connection) HandlePayload(addr *net.UDPAddr, payload []byte) {
	if len(payload) == 0 {
		return
	}
	msgType := MsgType(payload[0])

	if c.verbose {
		logPayload(addr, msgType, payload)
	}

	switch msgType {
	case MsgOpenReq:
		c.handleOpen(addr, payload)
	case MsgCloseReq:
		c.handleClose(addr, payload)
	case MsgReadReq:
		c.handleRead(addr, payload)
	case MsgWriteReq:
		c.handleWriteReq(addr, payload)
	case MsgWriteData:
		c.handleWriteData(addr, payload)
	case MsgLseekReq:
		c.handleLseek(addr, payload)
	case MsgDreadReq:
		c.handleDread(addr, payload)
	case MsgGetstatReq:
		c.handleGetstat(addr, payload)
	case MsgMkdirReq:
		c.handleMkdir(addr, payload)
	case MsgRemoveReq:
		c.handleRemove(addr, payload)
	case MsgRmdirReq:
		c.handleRmdir(addr, payload)
	case MsgBreadReq:
		c.handleBread(addr, payload)
	case MsgBwriteReq:
		c.handleBwriteReq(addr, payload)
	default:
		log.Printf("[%s]: unknown message type: 0x%02x", addr, msgType)
		c.SendACK(addr, true)
	}
}

// handleOpen parses OPEN_REQ payload, calls fs.Open, sends OPEN_REPLY.
func (c *Connection) handleOpen(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 8 {
		c.SendOpenReply(addr, -22, StatInfo{})
		return
	}
	flags := binary.LittleEndian.Uint16(payload[2:4])
	pathEnd := bytes.IndexByte(payload[8:], 0)
	if pathEnd < 0 {
		pathEnd = len(payload) - 8
	}
	path := string(payload[8 : 8+pathEnd])
	isDir := len(payload) > 1 && payload[1] != 0

	handle, stat, err := c.fs.Open(path, udpfsFlagsToOSFlags(flags), isDir)
	if err != nil {
		c.SendOpenReply(addr, -int32(errToErrno(err)), stat)
		return
	}
	if handle < 0 {
		c.SendOpenReply(addr, handle, stat)
		return
	}
	c.SendOpenReply(addr, handle, stat)
}

// handleClose parses CLOSE_REQ, calls fs.Close, sends CLOSE_REPLY.
func (c *Connection) handleClose(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 8 {
		c.SendCloseReply(addr, -22)
		return
	}
	handle := int32(binary.LittleEndian.Uint32(payload[4:8]))

	if handle == BlockDeviceHandle {
		c.SendCloseReply(addr, 0)
		return
	}
	if err := c.fs.Close(handle); err != nil {
		c.SendCloseReply(addr, -int32(errToErrno(err)))
		return
	}
	c.SendCloseReply(addr, 0)
}

// handleRead parses READ_REQ, calls fs.Read, sends RESULT_REPLY + data.
func (c *Connection) handleRead(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 12 {
		c.SendReadResult(addr, -22, nil)
		return
	}
	handle := int32(binary.LittleEndian.Uint32(payload[4:8]))
	size := binary.LittleEndian.Uint32(payload[8:12])

	n, data, err := c.fs.Read(handle, size)
	if err != nil {
		c.SendReadResult(addr, -int32(errToErrno(err)), nil)
		return
	}
	c.SendReadResult(addr, n, data)
}

// handleWriteReq parses WRITE_REQ, calls fs.WriteStart; if first chunk in payload, parses and calls WriteChunk; if done, calls fs.CompleteWrite and sends WRITE_DONE.
func (c *Connection) handleWriteReq(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 12 {
		c.SendACK(addr, true)
		return
	}
	handle := int32(binary.LittleEndian.Uint32(payload[4:8]))

	if err := c.fs.WriteStart(handle); err != nil {
		c.SendWriteDone(addr, -int32(errToErrno(err)))
		return
	}
	if len(payload) <= 12 {
		c.SendACK(addr, true)
		return
	}
	// First chunk in same packet: bytes 12+ are chunk (reserved(2), chunkNr(2), chunkSize(2), totalChunks(2), data)
	chunkPayload := payload[12:]
	if len(chunkPayload) < 8 {
		c.SendACK(addr, true)
		return
	}
	chunkNr := binary.LittleEndian.Uint16(chunkPayload[2:4])
	chunkSize := binary.LittleEndian.Uint16(chunkPayload[4:6])
	totalChunks := binary.LittleEndian.Uint16(chunkPayload[6:8])
	chunkData := chunkPayload[8:]
	if int(chunkSize) < len(chunkData) {
		chunkData = chunkData[:chunkSize]
	}
	done, err := c.fs.WriteChunk(handle, chunkNr, chunkSize, totalChunks, chunkData)
	if err != nil {
		c.SendWriteDone(addr, -int32(errToErrno(err)))
		return
	}
	if done {
		n, err := c.fs.CompleteWrite(handle)
		if err != nil {
			c.SendWriteDone(addr, -int32(errToErrno(err)))
			return
		}
		c.SendWriteDone(addr, n)
		return
	}
	c.SendACK(addr, true)
}

// handleWriteData parses WRITE_DATA chunk, calls fs.WriteChunk; if done, calls fs.CompleteWrite and sends WRITE_DONE.
func (c *Connection) handleWriteData(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 8 {
		c.SendACK(addr, true)
		return
	}
	handle := int32(binary.LittleEndian.Uint32(payload[4:8]))
	chunkNr := binary.LittleEndian.Uint16(payload[2:4])
	chunkSize := binary.LittleEndian.Uint16(payload[4:6])
	totalChunks := binary.LittleEndian.Uint16(payload[6:8])
	chunkData := payload[8:]
	if int(chunkSize) < len(chunkData) {
		chunkData = chunkData[:chunkSize]
	}

	done, err := c.fs.WriteChunk(handle, chunkNr, chunkSize, totalChunks, chunkData)
	if err != nil {
		c.SendWriteDone(addr, -int32(errToErrno(err)))
		return
	}
	if done {
		n, err := c.fs.CompleteWrite(handle)
		if err != nil {
			c.SendWriteDone(addr, -int32(errToErrno(err)))
			return
		}
		c.SendWriteDone(addr, n)
		return
	}
	c.SendACK(addr, true)
}

// handleLseek parses LSEEK_REQ, calls fs.Lseek, sends LSEEK_REPLY.
func (c *Connection) handleLseek(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 16 {
		c.SendLseekReply(addr, -1)
		return
	}
	handle := int32(binary.LittleEndian.Uint32(payload[4:8]))
	offsetLo := binary.LittleEndian.Uint32(payload[8:12])
	offsetHi := binary.LittleEndian.Uint32(payload[12:16])
	offset := int64(offsetHi)<<32 | int64(offsetLo)
	whence := int(payload[1])

	pos, err := c.fs.Lseek(handle, offset, whence)
	if err != nil {
		c.SendLseekReply(addr, -1)
		return
	}
	c.SendLseekReply(addr, pos)
}

// handleDread parses DREAD_REQ, calls fs.Dread, sends DREAD_REPLY.
func (c *Connection) handleDread(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 8 {
		c.SendDreadReply(addr, -22, "", StatInfo{})
		return
	}
	handle := int32(binary.LittleEndian.Uint32(payload[4:8]))

	ok, name, stat, err := c.fs.Dread(handle)
	if err != nil {
		c.SendDreadReply(addr, -int32(errToErrno(err)), "", StatInfo{})
		return
	}
	if !ok {
		c.SendDreadReply(addr, 0, "", StatInfo{})
		return
	}
	c.SendDreadReply(addr, 1, name, stat)
}

// handleGetstat parses GETSTAT_REQ, calls fs.Getstat, sends GETSTAT_REPLY.
func (c *Connection) handleGetstat(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 4 {
		c.SendGetstatReply(addr, -22, StatInfo{})
		return
	}
	pathEnd := bytes.IndexByte(payload[4:], 0)
	if pathEnd < 0 {
		pathEnd = len(payload) - 4
	}
	path := string(payload[4 : 4+pathEnd])

	stat, err := c.fs.Getstat(path)
	if err != nil {
		c.SendGetstatReply(addr, -int32(errToErrno(err)), StatInfo{})
		return
	}
	c.SendGetstatReply(addr, 0, stat)
}

// handleMkdir parses MKDIR_REQ, calls fs.Mkdir, sends RESULT_REPLY.
func (c *Connection) handleMkdir(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 4 {
		c.SendResultReplyOnly(addr, -22)
		return
	}
	pathEnd := bytes.IndexByte(payload[4:], 0)
	if pathEnd < 0 {
		pathEnd = len(payload) - 4
	}
	path := string(payload[4 : 4+pathEnd])

	if err := c.fs.Mkdir(path); err != nil {
		c.SendResultReplyOnly(addr, -int32(errToErrno(err)))
		return
	}
	c.SendResultReplyOnly(addr, 0)
}

// handleRemove parses REMOVE_REQ, calls fs.Remove, sends RESULT_REPLY.
func (c *Connection) handleRemove(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 4 {
		c.SendResultReplyOnly(addr, -22)
		return
	}
	pathEnd := bytes.IndexByte(payload[4:], 0)
	if pathEnd < 0 {
		pathEnd = len(payload) - 4
	}
	path := string(payload[4 : 4+pathEnd])

	if err := c.fs.Remove(path); err != nil {
		c.SendResultReplyOnly(addr, -int32(errToErrno(err)))
		return
	}
	c.SendResultReplyOnly(addr, 0)
}

// handleRmdir parses RMDIR_REQ, calls fs.Rmdir, sends RESULT_REPLY.
func (c *Connection) handleRmdir(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 4 {
		c.SendResultReplyOnly(addr, -22)
		return
	}
	pathEnd := bytes.IndexByte(payload[4:], 0)
	if pathEnd < 0 {
		pathEnd = len(payload) - 4
	}
	path := string(payload[4 : 4+pathEnd])

	if err := c.fs.Rmdir(path); err != nil {
		c.SendResultReplyOnly(addr, -int32(errToErrno(err)))
		return
	}
	c.SendResultReplyOnly(addr, 0)
}

// handleBread parses BREAD_REQ, calls fs.Bread, sends RESULT_REPLY + data.
func (c *Connection) handleBread(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 16 {
		c.SendReadResult(addr, -22, nil)
		return
	}
	sectorCount := binary.LittleEndian.Uint16(payload[2:4])
	handle := int32(binary.LittleEndian.Uint32(payload[4:8]))
	sectorNrLo := binary.LittleEndian.Uint32(payload[8:12])
	sectorNrHi := binary.LittleEndian.Uint32(payload[12:16])
	sectorNr := int64(sectorNrHi)<<32 | int64(sectorNrLo)

	data, err := c.fs.Bread(handle, sectorNr, sectorCount)
	if err != nil {
		c.SendReadResult(addr, -int32(errToErrno(err)), nil)
		return
	}
	c.SendReadResult(addr, int32(len(data)), data)
}

// handleBwriteReq parses BWRITE_REQ, calls fs.BwriteStart; if first chunk in payload, parses and calls fs.WriteChunk; if done, calls fs.CompleteWrite and sends WRITE_DONE.
func (c *Connection) handleBwriteReq(addr *net.UDPAddr, payload []byte) {
	if len(payload) < 16 {
		c.SendACK(addr, true)
		return
	}
	sectorCount := binary.LittleEndian.Uint16(payload[2:4])
	handle := int32(binary.LittleEndian.Uint32(payload[4:8]))
	sectorNrLo := binary.LittleEndian.Uint32(payload[8:12])
	sectorNrHi := binary.LittleEndian.Uint32(payload[12:16])
	sectorNr := int64(sectorNrHi)<<32 | int64(sectorNrLo)

	if err := c.fs.BwriteStart(handle, sectorNr, sectorCount); err != nil {
		c.SendWriteDone(addr, -int32(errToErrno(err)))
		return
	}
	if len(payload) <= 16 {
		c.SendACK(addr, true)
		return
	}
	chunkPayload := payload[16:]
	if len(chunkPayload) < 8 {
		c.SendACK(addr, true)
		return
	}
	chunkNr := binary.LittleEndian.Uint16(chunkPayload[2:4])
	chunkSize := binary.LittleEndian.Uint16(chunkPayload[4:6])
	totalChunks := binary.LittleEndian.Uint16(chunkPayload[6:8])
	chunkData := chunkPayload[8:]
	if int(chunkSize) < len(chunkData) {
		chunkData = chunkData[:chunkSize]
	}
	done, err := c.fs.WriteChunk(handle, chunkNr, chunkSize, totalChunks, chunkData)
	if err != nil {
		c.SendWriteDone(addr, -int32(errToErrno(err)))
		return
	}
	if done {
		n, err := c.fs.CompleteWrite(handle)
		if err != nil {
			c.SendWriteDone(addr, -int32(errToErrno(err)))
			return
		}
		c.SendWriteDone(addr, n)
		return
	}
	c.SendACK(addr, true)
}
