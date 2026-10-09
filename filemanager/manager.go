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
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	os.MkdirAll(root, 0755)
	return &Manager{
		Root:    root,
		MaxSize: 100 * 1024 * 1024, // 100 MB
	}
}

// sanitize maps a client path (relative to Root, "/" = Root) to an absolute
// path that is guaranteed to be inside Root.
func (m *Manager) sanitize(path string) (string, error) {
	// Cleaning as an absolute path resolves every ".." against "/", so the
	// result can never climb above Root.
	clean := filepath.Clean("/" + path)
	abs := filepath.Join(m.Root, clean)
	if abs != m.Root && !strings.HasPrefix(abs, m.Root+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal denied")
	}
	return abs, nil
}

// validName checks a single path element supplied by the client.
func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("invalid file name: %q", name)
	}
	return nil
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

	// Browsers may send a full client path; keep only the base name
	name := filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	if err := validName(name); err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	dst := filepath.Join(dir, name)

	// Write to a temp file and rename, so a failed upload never leaves a
	// truncated file in place of an existing one.
	tmp, err := os.CreateTemp(dir, "."+name+".upload-*")
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	tmpName := tmp.Name()

	written, err := io.Copy(tmp, io.LimitReader(file, m.MaxSize+1))
	if err == nil && written > m.MaxSize {
		err = fmt.Errorf("file too large (max %d bytes)", m.MaxSize)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmpName, 0644)
	}
	if err == nil {
		err = os.Rename(tmpName, dst)
	}
	if err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("write file: %w", err)
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

	if full == m.Root {
		return fmt.Errorf("cannot delete root directory")
	}
	if _, err := os.Lstat(full); err != nil {
		return err
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
	if oldFull == m.Root || newFull == m.Root {
		return fmt.Errorf("cannot rename root directory")
	}
	if err := validName(filepath.Base(newFull)); err != nil {
		return err
	}
	if _, err := os.Lstat(newFull); err == nil {
		return fmt.Errorf("target already exists")
	}
	return os.Rename(oldFull, newFull)
}
