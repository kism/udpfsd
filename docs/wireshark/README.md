# Wireshark dissector for UDPRDMA / UDPFS

Decodes UDP traffic for the UDPRDMA and UDPFS protocols.

- **UDPRDMA**: 2-byte base header (packet type + 12-bit seq_nr). Then:
  - **DISCOVERY (0)** / **INFORM (1)**: 4-byte header (service_id, reserved). Server IP is the packet source.
  - **DATA (2)**: 4-byte data header (seq_nr_ack, flags, app header words, data byte count), then payload (app header + data).
- **UDPFS**: Application messages inside DATA payload. First byte is message type (OPEN_REQ, READ_REQ, BREAD_REQ, etc.). All message layouts are dissected (paths, handles, sizes, chunk info, etc.).

## Installation

**Option A – Personal plugins (recommended)**

1. Create a plugins directory if needed:
   - **Windows**: `%APPDATA%\Wireshark\plugins`
   - **macOS**: `$HOME/.local/lib/wireshark/plugins`
   - **Linux**: `$HOME/.local/lib/wireshark/plugins` or `~/.wireshark/plugins`
2. Copy `udpfs_udprdma.lua` into that directory.
3. Restart Wireshark (or enable Lua in *Help → About Wireshark → Folders* and reload Lua).

**Option B – Load from project**

In Wireshark’s init file (e.g. `%APPDATA%\Wireshark\init.lua` or `~/.wireshark/init.lua`), add:

```lua
dofile("/path/to/udpfs-go/docs/wireshark/udpfs_udprdma.lua")
```

Then restart Wireshark or run *Analyze → Reload Lua Plugins*.

**After reloading Lua plugins:** Raw read-continuation detection depends on having seen the preceding RESULT_REPLY packet in the same flow. If dissection runs out of frame order, some DATA packets may be mis-labelled (e.g. as MKDIR_REQ or 0x??). Reload the capture (*File → Reload* or Ctrl+R) so packets are dissected in order; the issue then goes away.

## Display filters

- `udprdma` – all UDPRDMA
- `udprdma.packet_type == 0` – DISCOVERY
- `udprdma.packet_type == 1` – INFORM
- `udprdma.packet_type == 2` – DATA
- `udpfs` – UDPFS messages inside DATA
- `udpfs.msg_type == 0x10` – OPEN_REQ
- `udpfs.handle == 0` – block device or handle 0
- `udpfs.path` – packets that contain a path field
