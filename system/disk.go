//go:build linux

package system

import (
	"bufio"
	"os"
	"strings"
	"sync"
	"syscall"
)

type DiskInfo struct {
	Filesystem string  `json:"filesystem"`
	MountedOn  string  `json:"mountedOn"`
	Total      uint64  `json:"total"`
	Used       uint64  `json:"used"`
	Free       uint64  `json:"free"`
	Usage      float64 `json:"usage"`
}

type DiskStats struct {
	Disks []DiskInfo `json:"disks"`
}

type DiskCollector struct {
	mu    sync.RWMutex
	stats DiskStats
}

func NewDiskCollector() *DiskCollector {
	return &DiskCollector{}
}

func (c *DiskCollector) Collect() {
	disks := readDiskUsage()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.Disks = disks
}

func (c *DiskCollector) Stats() DiskStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.stats
	s.Disks = make([]DiskInfo, len(c.stats.Disks))
	copy(s.Disks, c.stats.Disks)
	return s
}

func readDiskUsage() []DiskInfo {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()

	var disks []DiskInfo
	seen := make(map[string]bool)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		fsType := fields[2]
		mountPoint := fields[1]
		device := fields[0]

		// Skip pseudo filesystems
		if !isRealFilesystem(fsType) {
			continue
		}
		// Skip bind mounts and duplicates
		if seen[mountPoint] {
			continue
		}
		seen[mountPoint] = true

		var stat syscall.Statfs_t
		if err := syscall.Statfs(mountPoint, &stat); err != nil {
			continue
		}

		total := stat.Blocks * uint64(stat.Bsize)
		free := stat.Bavail * uint64(stat.Bsize)
		if total == 0 {
			continue
		}
		used := total - free
		usage := float64(used) / float64(total) * 100

		disks = append(disks, DiskInfo{
			Filesystem: device,
			MountedOn:  mountPoint,
			Total:      total,
			Used:       used,
			Free:       free,
			Usage:      usage,
		})
	}

	return disks
}

func isRealFilesystem(fsType string) bool {
	switch fsType {
	case "ext2", "ext3", "ext4", "xfs", "btrfs", "jfs", "reiserfs",
		"vfat", "fat32", "ntfs", "exfat",
		"tmpfs", "ubifs", "squashfs", "overlay", "f2fs":
		return true
	}
	return false
}
