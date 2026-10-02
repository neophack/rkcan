//go:build linux

package replay

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/penghongxia/rkcan/canlog"
)

// Volume is a mounted filesystem that can hold log files.
type Volume struct {
	Device     string `json:"device"`
	MountPoint string `json:"mountPoint"`
	FSType     string `json:"fsType"`
	Label      string `json:"label"`
	Removable  bool   `json:"removable"`
	Total      uint64 `json:"total"`
	Free       uint64 `json:"free"`
}

// Card is a removable partition that is not mounted yet.
type Card struct {
	Device string `json:"device"`
	Size   uint64 `json:"size"`
	Label  string `json:"label"`
}

// LogFile is a CAN log found on a volume.
type LogFile struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
	Format  string `json:"format"`
}

// MountBase is where unmounted cards are mounted by MountCard.
var MountBase = "/mnt"

// ExtraDirs are always offered as storage (e.g. the file manager root).
var ExtraDirs []string

// blockDev returns the parent disk of a partition device ("mmcblk1p1" ->
// "mmcblk1", "sda1" -> "sda").
func blockDev(dev string) string {
	name := filepath.Base(dev)
	if strings.HasPrefix(name, "mmcblk") || strings.HasPrefix(name, "nvme") {
		if i := strings.LastIndex(name, "p"); i > 0 && i < len(name)-1 {
			return name[:i]
		}
		return name
	}
	return strings.TrimRight(name, "0123456789")
}

// isRemovableDisk reports whether a disk is an SD/TF card or a USB/removable drive.
func isRemovableDisk(disk string) bool {
	if strings.HasPrefix(disk, "mmcblk") {
		// eMMC reports "MMC", SD/TF cards report "SD"
		t, err := os.ReadFile(filepath.Join("/sys/block", disk, "device/type"))
		return err == nil && strings.TrimSpace(string(t)) == "SD"
	}
	if r, err := os.ReadFile(filepath.Join("/sys/block", disk, "removable")); err == nil && strings.TrimSpace(string(r)) == "1" {
		return true
	}
	// USB mass storage often reports removable=0; check the bus
	if link, err := filepath.EvalSymlinks(filepath.Join("/sys/block", disk)); err == nil && strings.Contains(link, "/usb") {
		return true
	}
	return false
}

func label(dev string) string {
	entries, err := os.ReadDir("/dev/disk/by-label")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		target, err := filepath.EvalSymlinks(filepath.Join("/dev/disk/by-label", e.Name()))
		if err == nil && target == dev {
			return strings.ReplaceAll(e.Name(), `\x20`, " ")
		}
	}
	return ""
}

func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

type mountEntry struct{ dev, dir, fstype string }

func readMounts() []mountEntry {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []mountEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) >= 3 {
			out = append(out, mountEntry{unescapeMount(fl[0]), unescapeMount(fl[1]), fl[2]})
		}
	}
	return out
}

func statfs(dir string) (total, free uint64) {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return 0, 0
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize)
}

// Volumes lists mounted removable volumes plus ExtraDirs.
func Volumes() []Volume {
	var vols []Volume
	seen := map[string]bool{}
	for _, m := range readMounts() {
		if !strings.HasPrefix(m.dev, "/dev/") || seen[m.dir] {
			continue
		}
		if !isRemovableDisk(blockDev(m.dev)) {
			continue
		}
		seen[m.dir] = true
		total, free := statfs(m.dir)
		vols = append(vols, Volume{Device: m.dev, MountPoint: m.dir, FSType: m.fstype,
			Label: label(m.dev), Removable: true, Total: total, Free: free})
	}
	for _, d := range ExtraDirs {
		if d == "" || seen[d] {
			continue
		}
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		seen[d] = true
		total, free := statfs(d)
		vols = append(vols, Volume{MountPoint: d, Total: total, Free: free})
	}
	return vols
}

// UnmountedCards lists removable partitions (or whole unpartitioned cards)
// that are not mounted.
func UnmountedCards() []Card {
	mounted := map[string]bool{}
	for _, m := range readMounts() {
		mounted[m.dev] = true
	}
	var cards []Card
	disks, _ := os.ReadDir("/sys/block")
	for _, d := range disks {
		disk := d.Name()
		if strings.HasPrefix(disk, "loop") || strings.HasPrefix(disk, "ram") || strings.HasPrefix(disk, "zram") ||
			strings.Contains(disk, "boot") || strings.Contains(disk, "rpmb") || !isRemovableDisk(disk) {
			continue
		}
		parts, _ := filepath.Glob(filepath.Join("/sys/block", disk, disk+"*"))
		if len(parts) == 0 {
			parts = []string{filepath.Join("/sys/block", disk)} // no partition table
		}
		for _, p := range parts {
			dev := "/dev/" + filepath.Base(p)
			if mounted[dev] {
				continue
			}
			var size uint64
			if b, err := os.ReadFile(filepath.Join(p, "size")); err == nil {
				fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &size)
				size *= 512
			}
			if size == 0 {
				continue // empty slot / no medium
			}
			cards = append(cards, Card{Device: dev, Size: size, Label: label(dev)})
		}
	}
	return cards
}

// MountCard mounts a removable partition under MountBase and returns the
// mount point.
func MountCard(dev string) (string, error) {
	found := false
	for _, c := range UnmountedCards() {
		if c.Device == dev {
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("%s is not an unmounted removable device", dev)
	}

	dir := filepath.Join(MountBase, "sdcard")
	if strings.HasPrefix(filepath.Base(dev), "sd") {
		dir = filepath.Join(MountBase, "usb-"+filepath.Base(dev))
	}
	if isMountPoint(dir) {
		dir = filepath.Join(MountBase, filepath.Base(dev))
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	// Let mount(8) detect the filesystem (vfat, exfat, ext4, ntfs...)
	out, err := exec.Command("mount", dev, dir).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mount %s: %v (%s)", dev, err, strings.TrimSpace(string(out)))
	}
	return dir, nil
}

func isMountPoint(dir string) bool {
	for _, m := range readMounts() {
		if m.dir == dir {
			return true
		}
	}
	return false
}

// Unmount flushes and unmounts a removable volume so the card can be removed.
func Unmount(dir string) error {
	ok := false
	for _, v := range Volumes() {
		if v.Removable && v.MountPoint == dir {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("%s is not a mounted removable volume", dir)
	}
	syscall.Sync()
	if out, err := exec.Command("umount", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("umount %s: %v (%s) — stop replay/recording first", dir, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AllowedPath reports whether path lies inside one of the current volumes.
func AllowedPath(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	for _, v := range Volumes() {
		root := filepath.Clean(v.MountPoint)
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// FindLogs lists .asc/.blf files below root (newest first), descending at
// most maxDepth directories and returning at most maxFiles entries.
func FindLogs(root string, maxDepth, maxFiles int) []LogFile {
	var out []LogFile
	root = filepath.Clean(root)
	base := strings.Count(root, string(filepath.Separator))
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "System Volume Information" || name == "lost+found") {
				return fs.SkipDir
			}
			if strings.Count(path, string(filepath.Separator))-base >= maxDepth {
				return fs.SkipDir
			}
			return nil
		}
		format := canlog.FormatOf(path)
		if format == "" || strings.HasPrefix(d.Name(), "._") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, LogFile{
			Path:    path,
			Name:    d.Name(),
			Dir:     filepath.Dir(path),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
			Format:  string(format),
		})
		if len(out) >= maxFiles {
			return fs.SkipAll
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		ti, _ := time.Parse("2006-01-02 15:04:05", out[i].ModTime)
		tj, _ := time.Parse("2006-01-02 15:04:05", out[j].ModTime)
		return ti.After(tj)
	})
	return out
}
