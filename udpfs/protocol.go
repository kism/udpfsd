package udpfs

// MsgType is the UDPFS message type (first byte of payload)
type MsgType uint8

const (
	// File operations
	MsgOpenReq      MsgType = 0x10
	MsgOpenReply    MsgType = 0x11
	MsgCloseReq     MsgType = 0x12
	MsgCloseReply   MsgType = 0x13
	MsgReadReq      MsgType = 0x14
	MsgWriteReq     MsgType = 0x16
	MsgWriteData    MsgType = 0x17
	MsgWriteDone    MsgType = 0x18
	MsgLseekReq     MsgType = 0x1A
	MsgLseekReply   MsgType = 0x1B
	MsgDreadReq     MsgType = 0x1C
	MsgDreadReply   MsgType = 0x1D
	MsgGetstatReq   MsgType = 0x1E
	MsgGetstatReply MsgType = 0x1F
	MsgMkdirReq     MsgType = 0x20
	MsgRemoveReq    MsgType = 0x22
	MsgRmdirReq     MsgType = 0x24
	MsgResultReply  MsgType = 0x26
	// Block I/O (UDPBD subset)
	MsgBreadReq  MsgType = 0x28
	MsgBwriteReq MsgType = 0x2A
)

// PS2 file mode flags
const (
	FIO_S_IFREG = 0x2000
	FIO_S_IFDIR = 0x1000
)

// Limits
const (
	WindowAckTimeoutSec = 0.1
	MaxWindowRetries    = 4
	BlockDeviceHandle   = 0
)
