//go:build linux

package serial

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type Config struct {
	Port     string `json:"port"`
	BaudRate int    `json:"baudRate"`
	DataBits int    `json:"dataBits"`
	StopBits int    `json:"stopBits"`
	Parity   string `json:"parity"` // "none", "even", "odd"
}

type LogLine struct {
	Time string `json:"time"`
	Data string `json:"data"`
}

type Reader struct {
	mu       sync.RWMutex
	file     *os.File
	config   Config
	lines    []LogLine
	maxLines int
	running  bool
	cancel   context.CancelFunc

	subscribers map[int]chan LogLine
	subMu       sync.Mutex
	nextSubID   int
}

func NewReader() *Reader {
	return &Reader{
		maxLines:    10000,
		lines:       make([]LogLine, 0, 1000),
		subscribers: make(map[int]chan LogLine),
	}
}

func ListPorts() []string {
	var ports []string

	patterns := []string{
		"/dev/ttyS*",
		"/dev/ttyUSB*",
		"/dev/ttyACM*",
		"/dev/ttyAMA*",
	}

	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if _, err := os.Stat(m); err == nil {
				ports = append(ports, m)
			}
		}
	}
	return ports
}

func (r *Reader) Open(cfg Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return fmt.Errorf("port already open")
	}

	if cfg.DataBits == 0 {
		cfg.DataBits = 8
	}
	if cfg.StopBits == 0 {
		cfg.StopBits = 1
	}
	if cfg.Parity == "" {
		cfg.Parity = "none"
	}
	if cfg.BaudRate == 0 {
		cfg.BaudRate = 115200
	}

	f, err := os.OpenFile(cfg.Port, os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", cfg.Port, err)
	}

	fd := int(f.Fd())
	if err := configurePort(fd, cfg); err != nil {
		f.Close()
		return err
	}

	// Keep O_NONBLOCK so readLoop can poll without blocking forever
	r.file = f
	r.config = cfg
	r.running = true
	r.lines = r.lines[:0]

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.readLoop(ctx)

	return nil
}

func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.running {
		return nil
	}
	r.running = false
	if r.cancel != nil {
		r.cancel()
	}
	if r.file != nil {
		r.file.Close()
		r.file = nil
	}
	return nil
}

func (r *Reader) IsOpen() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.running
}

func (r *Reader) GetConfig() Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config
}

func (r *Reader) GetLines(last int) []LogLine {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if last <= 0 || last > len(r.lines) {
		result := make([]LogLine, len(r.lines))
		copy(result, r.lines)
		return result
	}
	start := len(r.lines) - last
	result := make([]LogLine, last)
	copy(result, r.lines[start:])
	return result
}

func (r *Reader) Subscribe() (int, chan LogLine) {
	r.subMu.Lock()
	defer r.subMu.Unlock()

	id := r.nextSubID
	r.nextSubID++
	ch := make(chan LogLine, 100)
	r.subscribers[id] = ch
	return id, ch
}

func (r *Reader) Unsubscribe(id int) {
	r.subMu.Lock()
	defer r.subMu.Unlock()

	if ch, ok := r.subscribers[id]; ok {
		close(ch)
		delete(r.subscribers, id)
	}
}

func (r *Reader) readLoop(ctx context.Context) {
	fd := int(r.file.Fd())
	buf := make([]byte, 4096)
	var lineBuf strings.Builder

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := unix.Read(fd, buf)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			// EBADF or other error after close
			return
		}
		if n == 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		// Split incoming bytes into lines
		for i := 0; i < n; i++ {
			b := buf[i]
			if b == '\n' {
				r.emitLine(lineBuf.String())
				lineBuf.Reset()
			} else if b != '\r' {
				lineBuf.WriteByte(b)
			}
		}
	}
}

func (r *Reader) emitLine(text string) {
	line := LogLine{
		Time: time.Now().Format("15:04:05.000"),
		Data: text,
	}

	r.mu.Lock()
	r.lines = append(r.lines, line)
	if len(r.lines) > r.maxLines {
		r.lines = r.lines[len(r.lines)-r.maxLines:]
	}
	r.mu.Unlock()

	r.subMu.Lock()
	for _, ch := range r.subscribers {
		select {
		case ch <- line:
		default:
		}
	}
	r.subMu.Unlock()
}

func configurePort(fd int, cfg Config) error {
	var termios unix.Termios
	if _, _, errno := unix.Syscall6(unix.SYS_IOCTL, uintptr(fd),
		uintptr(unix.TCGETS), uintptr(unsafe.Pointer(&termios)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("TCGETS: %w", errno)
	}

	// Raw mode
	termios.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	termios.Oflag &^= unix.OPOST
	termios.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	termios.Cflag &^= unix.CSIZE | unix.PARENB

	// Data bits
	switch cfg.DataBits {
	case 5:
		termios.Cflag |= unix.CS5
	case 6:
		termios.Cflag |= unix.CS6
	case 7:
		termios.Cflag |= unix.CS7
	default:
		termios.Cflag |= unix.CS8
	}

	// Stop bits
	if cfg.StopBits == 2 {
		termios.Cflag |= unix.CSTOPB
	} else {
		termios.Cflag &^= unix.CSTOPB
	}

	// Parity
	switch strings.ToLower(cfg.Parity) {
	case "even":
		termios.Cflag |= unix.PARENB
		termios.Cflag &^= unix.PARODD
	case "odd":
		termios.Cflag |= unix.PARENB | unix.PARODD
	default:
		termios.Cflag &^= unix.PARENB
	}

	termios.Cflag |= unix.CLOCAL | unix.CREAD

	// Baud rate
	speed := baudToSpeed(cfg.BaudRate)
	termios.Ispeed = speed
	termios.Ospeed = speed

	// VMIN=1, VTIME=1 (100ms timeout)
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 1

	if _, _, errno := unix.Syscall6(unix.SYS_IOCTL, uintptr(fd),
		uintptr(unix.TCSETS), uintptr(unsafe.Pointer(&termios)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("TCSETS: %w", errno)
	}

	return nil
}

func baudToSpeed(baud int) uint32 {
	switch baud {
	case 9600:
		return unix.B9600
	case 19200:
		return unix.B19200
	case 38400:
		return unix.B38400
	case 57600:
		return unix.B57600
	case 115200:
		return unix.B115200
	case 230400:
		return unix.B230400
	case 460800:
		return unix.B460800
	case 500000:
		return unix.B500000
	case 576000:
		return unix.B576000
	case 921600:
		return unix.B921600
	case 1000000:
		return unix.B1000000
	case 1500000:
		return unix.B1500000
	case 2000000:
		return unix.B2000000
	default:
		return unix.B115200
	}
}
