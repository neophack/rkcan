//go:build linux

package filemanager

import (
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"isDir"`
	ModTime string `json:"modTime"`
	Mode    string `json:"mode"`
}

type Manager struct {
	Root    string
	MaxSize int64
}

func NewManager(root string) *Manager {
	if root == "" {
		root = "/userdata"
	}
	os.MkdirAll(root, 0755)
	return &Manager{
		Root:    root,
		MaxSize: 100 * 1024 * 1024, // 100 MB
	}
}

func (m *Manager) sanitize(path string) (string, error) {
	clean := filepath.Clean(path)
	if clean == "" || clean == "." {
		clean = "/"
	}
	full := filepath.Join(m.Root, clean)
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	rootAbs, _ := filepath.Abs(m.Root)
	if !strings.HasPrefix(abs, rootAbs) {
		return "", fmt.Errorf("path traversal denied")
	}
	return abs, nil
}

func (m *Manager) List(path string) ([]FileInfo, error) {
	dir, err := m.sanitize(path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	files := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		relPath, _ := filepath.Rel(m.Root, filepath.Join(dir, e.Name()))
		files = append(files, FileInfo{
			Name:    e.Name(),
			Path:    "/" + filepath.ToSlash(relPath),
			Size:    info.Size(),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
			Mode:    info.Mode().String(),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return files[i].Name < files[j].Name
	})

	return files, nil
}

func (m *Manager) Upload(path string, file multipart.File, header *multipart.FileHeader) error {
	if header.Size > m.MaxSize {
		return fmt.Errorf("file too large: %d bytes (max %d)", header.Size, m.MaxSize)
	}

	dir, err := m.sanitize(path)
	if err != nil {
		return err
	}

	os.MkdirAll(dir, 0755)
	dst := filepath.Join(dir, header.Filename)

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	written, err := io.Copy(out, file)
	if err != nil {
		os.Remove(dst)
		return fmt.Errorf("write file: %w", err)
	}
	if written != header.Size {
		// Not fatal, just a mismatch in reported size
	}

	return nil
}

func (m *Manager) Download(path string) (string, *os.File, error) {
	full, err := m.sanitize(path)
	if err != nil {
		return "", nil, err
	}

	info, err := os.Stat(full)
	if err != nil {
		return "", nil, err
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("cannot download directory")
	}

	f, err := os.Open(full)
	if err != nil {
		return "", nil, err
	}

	return info.Name(), f, nil
}

func (m *Manager) Delete(path string) error {
	full, err := m.sanitize(path)
	if err != nil {
		return err
	}

	rootAbs, _ := filepath.Abs(m.Root)
	if full == rootAbs {
		return fmt.Errorf("cannot delete root directory")
	}

	return os.RemoveAll(full)
}

func (m *Manager) Mkdir(path string) error {
	full, err := m.sanitize(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(full, 0755)
}

func (m *Manager) Rename(oldPath, newPath string) error {
	oldFull, err := m.sanitize(oldPath)
	if err != nil {
		return err
	}
	newFull, err := m.sanitize(newPath)
	if err != nil {
		return err
	}
	return os.Rename(oldFull, newFull)
}

func (m *Manager) GetDiskUsage() (total, used, free uint64) {
	// This is a simplified version. Full implementation would use syscall.Statfs
	var stat [5]uint64 // placeholder
	_ = stat
	return 0, 0, 0
}

type DiskInfo struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Free      uint64  `json:"free"`
	UsagePct  float64 `json:"usagePct"`
	MountPath string  `json:"mountPath"`
}

func (m *Manager) DiskUsage() DiskInfo {
	// Use Statfs syscall
	info := DiskInfo{MountPath: m.Root}

	f, err := os.Open(m.Root)
	if err != nil {
		return info
	}
	f.Close()

	// Read from df command as fallback
	return info
}

func FormatTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}
