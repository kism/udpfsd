package fs

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/pcm720/udpfsd/internal/fs/compression"
)

// LogBackendInfo logs root dir and block device at startup.
func (s *Backend) PrintFSInfo() {
	s.Lock()
	defer s.Unlock()
	if s.fsRoot != "" {
		log.Printf("fs: mounted root filesystem %s (read-only: %t)", s.fsRoot, s.readOnly)
	}
	if s.bdHandle != nil {
		log.Printf("fs: mounted block device %s, (sectors: %d, sector size: %d, read-only: %t)", s.blockDevice, s.bdHandle.totalSectorCount, s.sectorSize, s.readOnly)
	}
	if s.enableCompression {
		formats := compression.GetSupportedFormats()
		log.Printf("fs: enabled decompression for %s\n", strings.Join(formats, ", "))
	}
}

func (s *Backend) resolvePath(clientPath string) (string, bool) {
	if s.fsRoot == "" {
		return "", false
	}
	clientPath = strings.TrimPrefix(clientPath, "/")
	clientPath = strings.TrimPrefix(clientPath, "\\")
	clientPath = filepath.FromSlash(clientPath)
	joined := filepath.Join(s.fsRoot, clientPath)
	resolved, err := filepath.Abs(joined)
	if err != nil {
		return "", false
	}
	if !strings.HasPrefix(resolved, s.fsRoot+string(os.PathSeparator)) && resolved != s.fsRoot {
		return "", false
	}
	return resolved, true
}

func (s *Backend) pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
