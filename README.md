# udpfsd — UDPFS server

A Go implementation of the UDPFS/UDPRDMA server used for serving filesystems and block devices to a PlayStation 2 over the network.

## Overview

### Server Features

- UDPFS and UDPBD (as UDPFS subset) protocols
- Serve a filesystem directory, a block device image, or both at once
- On-the-fly decompression of CSO, ZSO, and CHD disc images
- Multiple concurrent clients
- Read-only mode

This server is based on the original [UDPFS server from Neutrino](https://github.com/rickgaiser/neutrino/blob/master/pc/udpfs_server.py), written in Python.  
The Go port keeps the same protocol and feature set; compared to the original, it adds:

- Isolated session and handle state per client, so several PS2s can use the same server at once.
- Discovery and data handling run in separate goroutines, so one client’s traffic or decompression does not block others.
- More handles (64 vs 32) and automatic cleanup of idle peers and opened files.
- Single binary and environment-variable configuration for containers and embedded devices.

## Usage

### Prerequisites

- A directory containing files to serve (for filesystem mode)
- Optionally, a block device image for UDPBD over UDPRDMA
- `udpfsd` binary (or `udpfsd.exe` on Windows)

Pre-built archives are available in [Releases](https://github.com/pcm720/udpfsd/releases).  
Each archive contains a single binary named `udpfsd` (or `udpfsd.exe` on Windows) plus this README.  
Download the archive for your platform and extract it.

*`<version>` is the release tag (e.g. `v1.0.0`) or `nightly` for development builds.*

| Platform | Architecture | Release archive |
|----------|--------------|-------------------------|
| Linux    | AMD64       | `udpfsd-linux-amd64-<version>.zip` |
| Linux    | ARM 64-bit   | `udpfsd-linux-arm64-<version>.zip` |
| Linux    | ARM v7 (32-bit) | `udpfsd-linux-armv7-<version>.zip` |
| Linux    | ARM v6 (32-bit) | `udpfsd-linux-armv6-<version>.zip` |
| macOS    | AMD64       | `udpfsd-macos-amd64-<version>.zip` |
| macOS    | ARM 64-bit (Apple Silicon) | `udpfsd-macos-arm64-<version>.zip` |
| Windows  | AMD64       | `udpfsd-windows-amd64-<version>.zip` |
| Windows  | ARM 64-bit   | `udpfsd-windows-arm64-<version>.zip` |

> **Note:** Pre-built binaries support only CSO and ZSO.  
For CHD, build from source with CGO (see [building from source](#building-from-source)).

### Quick start

Serve a directory (Linux/macOS):
```bash
$ udpfsd -fsroot /path/to/files
```

Windows (PowerShell or cmd):
```powershell
> udpfsd.exe -fsroot C:\path\to\files
```

Or serve a block device image:
```bash
$ udpfsd -bdpath /path/to/image.chd
```

At least one of `-fsroot` or `-bdpath` is required; if you omit both, the server prints an error and exits. Both options can be used at the same time.

### Configuration

udpfsd supports both command-line options and environment variables.  
Environment variable names are the uppercase form of the flag name with hyphens replaced by underscores:

| Environment Variable | Flag | Description |
|---------------------|--------------------|-------------|
| `FSROOT` | `-fsroot` | Root directory to serve files from |
| `BDPATH` | `-bdpath` | Path to block device/image to serve |
| `PORT` | `-port` | UDP port for discovery and data (default: 62966) |
| `BIND` | `-bind` | Address and port for data connection, e.g. `0.0.0.0:62966` or `192.168.1.1:0` (default: `:0` = any port) |
| `SECTOR_SIZE` | `-sector-size` | Sector size for block device in bytes (default: 512) |
| `RO` | `-ro` | Serve in read-only mode |
| `VERBOSE` | `-verbose` | Enable verbose output |
| `COMPRESSION` | `-compression` | Enable transparent decompression for CHD/CSO/ZSO |
| `COMPRESSION_CACHE_SIZE` | `-compression-cache-size` | Number of cached blocks per file (default: 32) |

### Limitations

- Only one client may have a given file open for writing at a time.  
  While a file is open for writing, other clients cannot open that file for reading or writing.

### Examples

```bash
# Serve files from a specific directory in read-only mode
$ FSROOT=/mnt/files RO=1 udpfsd
$ udpfsd -fsroot /mnt/files -ro


# Serve with compression enabled and verbose logging
$ FSROOT=/path/to/files COMPRESSION=1 VERBOSE=1 udpfsd
$ udpfsd -fsroot /path/to/files -compression -verbose

# Bind to specific interface with read-only mode, serving both directory and block device
$ BDPATH=/mnt/exfat.img FSROOT=/shared BIND=192.168.1.100 RO=1 udpfsd
$ udpfsd -bdpath /mnt/exfat.img -fsroot /shared -bind 192.168.1.100 -ro


# Use environment variables instead of command-line arguments
export FSROOT=/mnt/files
export BDPATH=/images/exfat.img
export COMPRESSION_CACHE_SIZE=64
$ udpfsd
```

## Compression Support

When the `COMPRESSION` (or `-compression`) flag is enabled, the server transparently decompresses:

- **CHD**
- **CSO**
- **ZSO**

The decompression cache stores recently accessed blocks per file using an LRU strategy.  
The default cache size is 32 blocks, configurable via `COMPRESSION_CACHE_SIZE`.

## Protocol Details

For complete protocol specifications, see:

- [UDPRDMA Protocol](docs/UDPRDMA.md)
- [UDPFS Protocol](docs/UDPFS.md)
- [Wireshark dissector](docs/wireshark/)

## Building from source

### Prerequisites

- Go 1.25 or later
- libchdr for CHD support (optional, requires CGO)

### Building with CGO (Required for CHD support)

To build the server **with full CHD support**, you must:

1. Install `libchdr` on your system:
   - **Debian/Ubuntu**: `sudo apt install libchdr0 libchdr-dev`
   - **Arch Linux**: `libchdr-git` from AUR
   - Build from [source](https://github.com/rtissera/libchdr)

2. Enable CGO and build manually:
   ```bash
   export CGO_ENABLED=1
   go build -o bin/udpfsd ./cmd/udpfsd
   ```

### Building without CGO (CSO/ZSO only)

If you don't need CHD support:
```bash
CGO_ENABLED=0 go build -o bin/udpfsd ./cmd/udpfsd
```

To build all release targets (Linux, macOS, Windows; multiple architectures), run `make` from the repository root. Binaries are written to `build/`.

## Troubleshooting

- **Client can't connect** — Ensure the server discovery port (default 62966) is open in your firewall and that `-bind` matches an interface the client can reach. If required, set the port in the `-bind` argument (e.g. `-bind :41233`).
- **Client timeouts / "got unexpected sequence number"** — If the server has multiple interfaces on the same network (e.g., wired and Wi-Fi connections), the client can receive duplicate packets from different interfaces or bind to the wrong interface. The fix is to bind the data connection to a single interface: use `-bind` with that interface’s IP (e.g. `-bind 192.168.1.100` or `BIND=192.168.1.100`).
- **CHD not working** — Pre-built binaries don't include CHD; build from source with CGO and libchdr (see [Building from Source](#building-from-source)).

## License

See [LICENSE](LICENSE).

## Credits

- Rick Gaiser for [Neutrino](https://github.com/rickgaiser/neutrino) and UDPFS
