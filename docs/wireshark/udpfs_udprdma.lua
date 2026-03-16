--[[
  Wireshark dissector for UDPRDMA and UDPFS (PS2 file/block protocol over UDP).

  Aligned with docs/UDPRDMA.md and docs/UDPFS.md and udprdma, udpfs.

  UDPRDMA:
    - Port: 0xF5F6 (62966)
    - Base header 2 bytes (LE): packet_type (4 bits), seq_nr (12 bits)
    - DISCOVERY (0) / INFORM (1): +4 bytes (service_id 16, reserved 16). Server IP from packet source.
    - DATA (2): +4 bytes (seq_nr_ack 12, flags 2, hdr_word_count 4, data_byte_count 14), then payload

  UDPFS: Application messages inside DATA payload. First byte = message type.
]]

local UDPFS_PORT = 0xF5F6

-- Per-direction transfer state (UDPRDMA.md Variable Length Transfers, Receive Accumulation).
-- Key "src:srcport:dst:dstport" -> true when this sender is in a read/block response (we saw RESULT_REPLY with result>0); cleared on FIN.
-- While true, DATA packets with hdr_word_count=0 are raw continuation; otherwise they carry a single UDPFS message (e.g. client request).
-- Note: State is populated in dissection order. After reloading Lua plugins, dissection may run in an order where raw chunks are
-- processed before the RESULT_REPLY packet; if so, reload the capture (Ctrl+R) so packets are dissected in frame order.
local transfer_state = {}

local function conv_key(pinfo)
  return string.format("%s:%u:%s:%u", tostring(pinfo.src), pinfo.src_port, tostring(pinfo.dst), pinfo.dst_port)
end

-- UDPRDMA packet types (udprdma/protocol.go)
local PT_DISCOVERY = 0
local PT_INFORM    = 1
local PT_DATA     = 2

-- UDPRDMA data flags (DataFlagACK, DataFlagFIN) - docs/UDPRDMA.md
local DF_ACK = 1
local DF_FIN = 2

-- UDPFS message types (udpfs/protocol.go)
local UDPFS_MSG_NAMES = {
  [0x10] = "OPEN_REQ",
  [0x11] = "OPEN_REPLY",
  [0x12] = "CLOSE_REQ",
  [0x13] = "CLOSE_REPLY",
  [0x14] = "READ_REQ",
  [0x16] = "WRITE_REQ",
  [0x17] = "WRITE_DATA",
  [0x18] = "WRITE_DONE",
  [0x1A] = "LSEEK_REQ",
  [0x1B] = "LSEEK_REPLY",
  [0x1C] = "DREAD_REQ",
  [0x1D] = "DREAD_REPLY",
  [0x1E] = "GETSTAT_REQ",
  [0x1F] = "GETSTAT_REPLY",
  [0x20] = "MKDIR_REQ",
  [0x22] = "REMOVE_REQ",
  [0x24] = "RMDIR_REQ",
  [0x26] = "RESULT_REPLY",
  [0x28] = "BREAD_REQ",
  [0x2A] = "BWRITE_REQ",
}

-- Protocols
local udprdma_proto = Proto("udprdma", "UDPRDMA - Reliable RDMA over UDP")
local udpfs_proto   = Proto("udpfs",  "UDPFS - PS2 file/block over UDPRDMA")

-- UDPRDMA ProtoFields
local f_udprdma_type   = ProtoField.uint8("udprdma.packet_type", "Packet type", base.DEC,
  { [PT_DISCOVERY] = "DISCOVERY", [PT_INFORM] = "INFORM", [PT_DATA] = "DATA" })
local f_udprdma_seq    = ProtoField.uint16("udprdma.seq_nr", "Sequence number", base.DEC)
local f_udprdma_svc    = ProtoField.uint16("udprdma.service_id", "Service ID", base.HEX)
local f_udprdma_reserved = ProtoField.uint16("udprdma.reserved", "Reserved", base.HEX)
local f_udprdma_ack    = ProtoField.uint16("udprdma.seq_nr_ack", "Seq ACK/NACK", base.DEC)
local f_udprdma_flags  = ProtoField.uint8("udprdma.flags", "Flags", base.HEX,
  { [0] = "0", [1] = "ACK", [2] = "FIN", [3] = "ACK+FIN" })
local f_udprdma_hdr_words = ProtoField.uint8("udprdma.hdr_word_count", "App header words", base.DEC)
local f_udprdma_data_len  = ProtoField.uint16("udprdma.data_byte_count", "Data bytes", base.DEC)
local f_udprdma_payload   = ProtoField.bytes("udprdma.payload", "Payload")

udprdma_proto.fields = {
  f_udprdma_type, f_udprdma_seq, f_udprdma_svc, f_udprdma_reserved,
  f_udprdma_ack, f_udprdma_flags, f_udprdma_hdr_words, f_udprdma_data_len, f_udprdma_payload
}

-- UDPFS ProtoFields
local f_udpfs_msg_type   = ProtoField.uint8("udpfs.msg_type", "Message type", base.HEX)
local f_udpfs_is_dir     = ProtoField.uint8("udpfs.is_dir", "Is directory", base.DEC)
local f_udpfs_flags     = ProtoField.uint16("udpfs.flags", "Flags", base.HEX)
local f_udpfs_mode      = ProtoField.uint32("udpfs.mode", "Mode", base.HEX)
local f_udpfs_handle    = ProtoField.int32("udpfs.handle", "Handle", base.DEC)
local f_udpfs_result    = ProtoField.int32("udpfs.result", "Result", base.DEC)
local f_udpfs_size      = ProtoField.uint32("udpfs.size", "Size", base.DEC)
local f_udpfs_path      = ProtoField.string("udpfs.path", "Path")
local f_udpfs_chunk_nr  = ProtoField.uint16("udpfs.chunk_nr", "Chunk number", base.DEC)
local f_udpfs_chunk_size = ProtoField.uint16("udpfs.chunk_size", "Chunk size", base.DEC)
local f_udpfs_total_chunks = ProtoField.uint16("udpfs.total_chunks", "Total chunks", base.DEC)
local f_udpfs_whence    = ProtoField.uint8("udpfs.whence", "Whence", base.DEC, { [0] = "SEEK_SET", [1] = "SEEK_CUR", [2] = "SEEK_END" })
local f_udpfs_offset    = ProtoField.int64("udpfs.offset", "Offset", base.DEC)
local f_udpfs_position  = ProtoField.int64("udpfs.position", "Position", base.DEC)
local f_udpfs_sector_count = ProtoField.uint16("udpfs.sector_count", "Sector count", base.DEC)
local f_udpfs_sector_nr = ProtoField.int64("udpfs.sector_nr", "Sector number", base.DEC)
local f_udpfs_name_len  = ProtoField.uint16("udpfs.name_len", "Name length", base.DEC)
local f_udpfs_name      = ProtoField.string("udpfs.name", "Name")
local f_udpfs_attr      = ProtoField.uint32("udpfs.attr", "Attr", base.HEX)
local f_udpfs_data      = ProtoField.bytes("udpfs.data", "Data")

udpfs_proto.fields = {
  f_udpfs_msg_type, f_udpfs_is_dir, f_udpfs_flags, f_udpfs_mode, f_udpfs_handle, f_udpfs_result,
  f_udpfs_size, f_udpfs_path, f_udpfs_chunk_nr, f_udpfs_chunk_size, f_udpfs_total_chunks,
  f_udpfs_whence, f_udpfs_offset, f_udpfs_position, f_udpfs_sector_count, f_udpfs_sector_nr,
  f_udpfs_name_len, f_udpfs_name, f_udpfs_attr, f_udpfs_data
}

-- Parse null-terminated path from buf at offset, max 256 chars
local function parse_path(buf, off, max_len)
  local end_off = off
  while end_off < buf:len() and end_off - off < 256 do
    if buf(end_off, 1):uint() == 0 then break end
    end_off = end_off + 1
  end
  if end_off > buf:len() then end_off = buf:len() end
  if end_off <= off then return "", off end
  return buf(off, end_off - off):string(), end_off
end

-- Dissect one UDPFS message at payload buffer; returns bytes consumed (0 if truncated).
-- Only called when this payload is known to start with a UDPFS message (not raw continuation).
local function dissect_udpfs_message(pay_buf, pay_off, pay_len, tree)
  if pay_len < 1 then return 0 end
  local msg_type = pay_buf(pay_off, 1):uint()
  local msg_name = UDPFS_MSG_NAMES[msg_type] or string.format("0x%02X", msg_type)
  local st = tree:add(udpfs_proto, pay_buf(pay_off, math.min(pay_len, 256)), "UDPFS: " .. msg_name)
  st:add(f_udpfs_msg_type, pay_buf(pay_off, 1), msg_type)

  if msg_type == 0x10 then -- OPEN_REQ
    if pay_len < 8 then return 0 end
    st:add(f_udpfs_is_dir, pay_buf(pay_off + 1, 1), pay_buf(pay_off + 1, 1):uint())
    st:add_le(f_udpfs_flags, pay_buf(pay_off + 2, 2))
    st:add_le(f_udpfs_mode, pay_buf(pay_off + 4, 4))
    local path, _ = parse_path(pay_buf, pay_off + 8, 256)
    if path ~= "" then st:add(f_udpfs_path, path) end
    return 8 + math.min(pay_len - 8, 260)
  end

  if msg_type == 0x11 then -- OPEN_REPLY (36 bytes)
    if pay_len < 36 then return 0 end
    local handle = pay_buf(pay_off + 4, 4):le_int()
    st:add_le(f_udpfs_handle, pay_buf(pay_off + 4, 4), handle)
    st:add_le(f_udpfs_mode, pay_buf(pay_off + 8, 4))
    st:add_le(f_udpfs_size, pay_buf(pay_off + 12, 4))
    st:add_le(f_udpfs_size, pay_buf(pay_off + 16, 4)) -- hisize (reuse for display)
    return 36
  end

  if msg_type == 0x12 then -- CLOSE_REQ (8)
    if pay_len < 8 then return 0 end
    st:add_le(f_udpfs_handle, pay_buf(pay_off + 4, 4))
    return 8
  end

  if msg_type == 0x13 then -- CLOSE_REPLY (8)
    if pay_len < 8 then return 0 end
    st:add_le(f_udpfs_result, pay_buf(pay_off + 4, 4))
    return 8
  end

  if msg_type == 0x14 then -- READ_REQ (12)
    if pay_len < 12 then return 0 end
    st:add_le(f_udpfs_handle, pay_buf(pay_off + 4, 4))
    st:add_le(f_udpfs_size, pay_buf(pay_off + 8, 4))
    return 12
  end

  if msg_type == 0x16 then -- WRITE_REQ (12, optional WRITE_DATA after)
    if pay_len < 12 then return 0 end
    st:add_le(f_udpfs_handle, pay_buf(pay_off + 4, 4))
    st:add_le(f_udpfs_size, pay_buf(pay_off + 8, 4))
    local consumed = 12
    if pay_len >= 20 and pay_buf(pay_off + 12, 1):uint() == 0x17 then -- WRITE_DATA chunk in same packet
      local chunk_nr = pay_buf(pay_off + 14, 2):le_uint()
      local chunk_size = pay_buf(pay_off + 16, 2):le_uint()
      local total_chunks = pay_buf(pay_off + 18, 2):le_uint()
      local chunk = st:add(pay_buf(pay_off + 12, 8), string.format("WRITE_DATA: Chunk %u of %u (%u bytes)", chunk_nr, total_chunks, chunk_size))
      chunk:add_le(f_udpfs_chunk_nr, pay_buf(pay_off + 14, 2), chunk_nr)
      chunk:add_le(f_udpfs_chunk_size, pay_buf(pay_off + 16, 2), chunk_size)
      chunk:add_le(f_udpfs_total_chunks, pay_buf(pay_off + 18, 2), total_chunks)
      consumed = pay_len
      if pay_len > 20 then
        st:add(f_udpfs_data, pay_buf(pay_off + 20, pay_len - 20))
      end
    end
    return consumed
  end

  if msg_type == 0x17 then -- WRITE_DATA (8 + data); handle was in WRITE_REQ
    if pay_len < 8 then return 0 end
    local chunk_nr = pay_buf(pay_off + 2, 2):le_uint()
    local chunk_size = pay_buf(pay_off + 4, 2):le_uint()
    local total_chunks = pay_buf(pay_off + 6, 2):le_uint()
    st:add_le(f_udpfs_chunk_nr, pay_buf(pay_off + 2, 2), chunk_nr)
    st:add_le(f_udpfs_chunk_size, pay_buf(pay_off + 4, 2), chunk_size)
    st:add_le(f_udpfs_total_chunks, pay_buf(pay_off + 6, 2), total_chunks)
    st:add(pay_buf(pay_off, 8), string.format("Chunk %u of %u (%u bytes)", chunk_nr, total_chunks, chunk_size))
    if pay_len > 8 then
      st:add(f_udpfs_data, pay_buf(pay_off + 8, pay_len - 8))
    end
    return pay_len
  end

  if msg_type == 0x18 then -- WRITE_DONE (8)
    if pay_len < 8 then return 0 end
    st:add_le(f_udpfs_result, pay_buf(pay_off + 4, 4))
    return 8
  end

  if msg_type == 0x1A then -- LSEEK_REQ (16)
    if pay_len < 16 then return 0 end
    st:add(f_udpfs_whence, pay_buf(pay_off + 1, 1), pay_buf(pay_off + 1, 1):uint())
    st:add_le(f_udpfs_handle, pay_buf(pay_off + 4, 4))
    st:add_le(f_udpfs_offset, pay_buf(pay_off + 8, 8))
    return 16
  end

  if msg_type == 0x1B then -- LSEEK_REPLY (12)
    if pay_len < 12 then return 0 end
    st:add_le(f_udpfs_position, pay_buf(pay_off + 4, 8))
    return 12
  end

  if msg_type == 0x1C then -- DREAD_REQ (8)
    if pay_len < 8 then return 0 end
    st:add_le(f_udpfs_handle, pay_buf(pay_off + 4, 4))
    return 8
  end

  if msg_type == 0x1D then -- DREAD_REPLY (48 + name)
    if pay_len < 48 then return 0 end
    st:add_le(f_udpfs_name_len, pay_buf(pay_off + 2, 2))
    st:add_le(f_udpfs_result, pay_buf(pay_off + 4, 4))
    st:add_le(f_udpfs_mode, pay_buf(pay_off + 8, 4))
    st:add_le(f_udpfs_attr, pay_buf(pay_off + 12, 4))
    st:add_le(f_udpfs_size, pay_buf(pay_off + 16, 4))
    local name_len = pay_buf(pay_off + 2, 2):le_uint()
    if name_len > 0 and pay_len >= 48 + name_len then
      st:add(f_udpfs_name, pay_buf(pay_off + 48, name_len):string())
    end
    return 48 + math.min(math.max(name_len, 0), pay_len - 48)
  end

  if msg_type == 0x1E then -- GETSTAT_REQ (4 + path)
    if pay_len < 4 then return 0 end
    local path, _ = parse_path(pay_buf, pay_off + 4, 256)
    if path ~= "" then st:add(f_udpfs_path, path) end
    return 4 + math.min(pay_len - 4, 260)
  end

  if msg_type == 0x1F then -- GETSTAT_REPLY (48)
    if pay_len < 48 then return 0 end
    st:add_le(f_udpfs_result, pay_buf(pay_off + 4, 4))
    st:add_le(f_udpfs_mode, pay_buf(pay_off + 8, 4))
    st:add_le(f_udpfs_attr, pay_buf(pay_off + 12, 4))
    st:add_le(f_udpfs_size, pay_buf(pay_off + 16, 4))
    return 48
  end

  if msg_type == 0x20 or msg_type == 0x22 or msg_type == 0x24 then -- MKDIR_REQ / REMOVE_REQ / RMDIR_REQ (4 + path)
    if pay_len < 4 then return 0 end
    if msg_type == 0x20 then st:add_le(f_udpfs_mode, pay_buf(pay_off + 2, 2)) end
    local path, _ = parse_path(pay_buf, pay_off + 4, 256)
    if path ~= "" then st:add(f_udpfs_path, path) end
    return 4 + math.min(pay_len - 4, 260)
  end

  if msg_type == 0x26 then -- RESULT_REPLY (8), often followed by raw read/block data
    if pay_len < 8 then return 0 end
    local result = pay_buf(pay_off + 4, 4):le_int()
    st:add_le(f_udpfs_result, pay_buf(pay_off + 4, 4), result)
    if pay_len > 8 then
      local data_in_packet = pay_len - 8
      local total_read = (result > 0) and result or 0
      local data_tree = st:add(f_udpfs_data, pay_buf(pay_off + 8, data_in_packet))
      if total_read > 0 and data_in_packet > 0 then
        if data_in_packet < total_read then
          data_tree:append_text(string.format(" (%u of %u bytes in this packet)", data_in_packet, total_read))
        else
          data_tree:append_text(string.format(" (%u bytes)", data_in_packet))
        end
      end
    end
    return pay_len  -- rest is opaque data, do not parse as next message
  end

  if msg_type == 0x28 or msg_type == 0x2A then -- BREAD_REQ / BWRITE_REQ (16, optional WRITE_DATA after)
    if pay_len < 16 then return 0 end
    st:add_le(f_udpfs_sector_count, pay_buf(pay_off + 2, 2))
    st:add_le(f_udpfs_handle, pay_buf(pay_off + 4, 4))
    st:add_le(f_udpfs_sector_nr, pay_buf(pay_off + 8, 8))
    local consumed = 16
    if pay_len >= 24 and pay_buf(pay_off + 16, 1):uint() == 0x17 then -- WRITE_DATA chunk in same packet (BWRITE)
      local chunk_nr = pay_buf(pay_off + 18, 2):le_uint()
      local chunk_size = pay_buf(pay_off + 20, 2):le_uint()
      local total_chunks = pay_buf(pay_off + 22, 2):le_uint()
      local chunk = st:add(pay_buf(pay_off + 16, 8), string.format("WRITE_DATA: Chunk %u of %u (%u bytes)", chunk_nr, total_chunks, chunk_size))
      chunk:add_le(f_udpfs_chunk_nr, pay_buf(pay_off + 18, 2), chunk_nr)
      chunk:add_le(f_udpfs_chunk_size, pay_buf(pay_off + 20, 2), chunk_size)
      chunk:add_le(f_udpfs_total_chunks, pay_buf(pay_off + 22, 2), total_chunks)
      consumed = pay_len
      if pay_len > 24 then st:add(f_udpfs_data, pay_buf(pay_off + 24, pay_len - 24)) end
    end
    return consumed
  end

  -- Unknown type (should not happen when state-based raw continuation is used)
  st:set_text("UDPFS: Raw/continuation data")
  if pay_len > 1 then
    st:add(f_udpfs_data, pay_buf(pay_off + 1, pay_len - 1))
  end
  return pay_len
end

local function dissect_udprdma(buf, pinfo, tree)
  local len = buf:len()
  if len < 2 then return 0 end

  pinfo.cols.protocol:set("UDPRDMA")

  local base_val = buf(0, 2):le_uint()
  local ptype = bit.band(base_val, 0x0F)
  local seq   = bit.rshift(base_val, 4)
  local consumed = 2

  -- DISCOVERY / INFORM: base (2) + service_id (2) + reserved (2) = 6 bytes (docs/UDPRDMA.md)
  if ptype == PT_DISCOVERY or ptype == PT_INFORM then
    if len < 6 then
      pinfo.cols.info:set(string.format("UDPRDMA %s (truncated)", ptype == PT_DISCOVERY and "DISCOVERY" or "INFORM"))
      local root = tree:add(udprdma_proto, buf(0, len))
      root:add_le(f_udprdma_type, buf(0, 2), ptype)
      root:add_le(f_udprdma_seq, buf(0, 2), seq)
      return len
    end
    consumed = 6
    local root = tree:add(udprdma_proto, buf(0, 6))
    root:add_le(f_udprdma_type, buf(0, 2), ptype)
    root:add_le(f_udprdma_seq, buf(0, 2), seq)
    local disc_tree = root:add(buf(2, 4), "Discovery/Inform header")
    local svc = buf(2, 2):le_uint()
    local res = buf(4, 2):le_uint()
    disc_tree:add_le(f_udprdma_svc, buf(2, 2), svc)
    disc_tree:add_le(f_udprdma_reserved, buf(4, 2), res)
    pinfo.cols.info:set(string.format("UDPRDMA %s seq=%u service=0x%04X",
      ptype == PT_DISCOVERY and "DISCOVERY" or "INFORM", seq, svc))
    return consumed
  end

  -- DATA: base (2) + data header (4), then payload (hdr_word_count*4 + data_byte_count)
  if ptype == PT_DATA then
    if len < 6 then
      pinfo.cols.info:set("UDPRDMA DATA (truncated)")
      local root = tree:add(udprdma_proto, buf(0, len))
      root:add_le(f_udprdma_type, buf(0, 2), ptype)
      root:add_le(f_udprdma_seq, buf(0, 2), seq)
      return len
    end
    -- Data header: same bit layout as Go UnpackDataHeader (protocol.go) and UDPRDMA.md
    local dval = buf(2, 4):le_uint()
    local seq_ack   = bit.band(dval, 0xFFF)
    local flags     = bit.rshift(bit.band(dval, 0x3000), 12)
    local hdr_words = bit.rshift(bit.band(dval, 0x3C00), 14)
    local data_len  = bit.band(bit.rshift(dval, 18), 0x3FFF)
    local payload_len = hdr_words * 4 + data_len

    if 6 + payload_len > len then
      consumed = len
    else
      consumed = 6 + payload_len
    end

    local root = tree:add(udprdma_proto, buf(0, consumed))
    root:add_le(f_udprdma_type, buf(0, 2), ptype)
    root:add_le(f_udprdma_seq, buf(0, 2), seq)
    local dh = root:add(buf(2, 4), "Data header")
    dh:add_le(f_udprdma_ack, buf(2, 4), seq_ack)
    dh:add_le(f_udprdma_flags, buf(2, 4), flags)
    dh:add_le(f_udprdma_hdr_words, buf(2, 4), hdr_words)
    dh:add_le(f_udprdma_data_len, buf(2, 4), data_len)

    local fwd = conv_key(pinfo)

    if payload_len == 0 then
      pinfo.cols.info:set(string.format("UDPRDMA DATA seq=%u %s", seq, bit.band(flags, DF_ACK) ~= 0 and "ACK" or "NACK"))
    else
      local pay_buf = buf(6, math.min(payload_len, len - 6))
      root:add(f_udprdma_payload, pay_buf)

      if pay_buf:len() < 1 then
        pinfo.cols.info:set(string.format("UDPRDMA DATA seq=%u (payload truncated)", seq))
      else
        -- Payload: [app_header: hdr_words*4] [data: data_byte_count]. UDPRDMA.md Receive Accumulation: multi-packet transfer ends on FIN.
        -- hdr_words>0: app header present (UDPFS message, e.g. RESULT_REPLY); set transfer_state when read/block response has more data to come.
        -- hdr_words==0 and transfer_state[fwd]: we're in the middle of that transfer → raw continuation (do not interpret first byte as message type).
        -- hdr_words==0 and not transfer_state[fwd]: single UDPFS message in payload (e.g. client request: BREAD_REQ, READ_REQ, MKDIR_REQ).
        local in_read_response = transfer_state[fwd]
        local is_udpfs_message = (hdr_words > 0) or (not in_read_response)

        if is_udpfs_message then
          dissect_udpfs_message(pay_buf, 0, pay_buf:len(), root)
          local first_byte = pay_buf(0, 1):uint()
          local msg_name = UDPFS_MSG_NAMES[first_byte] or string.format("0x%02X", first_byte)
          pinfo.cols.info:set(string.format("UDPRDMA UDPFS seq=%u %s", seq, msg_name))
          if first_byte == 0x26 and pay_buf:len() >= 8 then
            local result = pay_buf(4, 4):le_int()
            if result > 0 then
              transfer_state[fwd] = true
            end
          end
        else
          -- Data chunk
          local pay_len = pay_buf:len()
          local st = root:add(udpfs_proto, pay_buf, "UDPFS: Raw data")
          st:set_text(string.format("UDPFS: Raw data (%u bytes)", pay_len))
          st:add(f_udpfs_data, pay_buf)
          if bit.band(flags, DF_FIN) ~= 0 then
            pinfo.cols.info:set(string.format("UDPRDMA UDPFS seq=%u Raw (data chunk, FIN)", seq))
          else
            pinfo.cols.info:set(string.format("UDPRDMA UDPFS seq=%u Raw (data chunk)", seq))
          end
        end

        if bit.band(flags, DF_FIN) ~= 0 then
          transfer_state[fwd] = nil
        end
      end
    end
    return consumed
  end

  local root = tree:add(udprdma_proto, buf(0, 2))
  root:add_le(f_udprdma_type, buf(0, 2), ptype)
  root:add_le(f_udprdma_seq, buf(0, 2), seq)
  pinfo.cols.info:set(string.format("UDPRDMA type=%u seq=%u", ptype, seq))
  return consumed
end

udprdma_proto.dissector = function(buf, pinfo, tree)
  if buf:len() < 2 then return 0 end
  return dissect_udprdma(buf, pinfo, tree)
end

local udp_port = DissectorTable.get("udp.port")
udp_port:add(UDPFS_PORT, udprdma_proto)
