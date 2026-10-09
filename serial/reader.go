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

// maxLineLen bounds a single line so a device that never sends '\n' cannot
// grow the line buffer without limit.
const maxLineLen = 4096

type Reader struct {
	mu       sync.RWMutex
	fd       int
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
		fd:          -1,
		maxLines:    10000,
		lines:       make([]LogLine, 0, 1000),
		subscribers: make(map[int]chan LogLine),
	}
}

var portPatterns = []string{
	"/dev/ttyS*",
	"/dev/ttyFIQ*",
	"/dev/ttyUSB*",
	"/dev/ttyACM*",
	"/dev/ttyAMA*",
}

func ListPorts() []string {
	ports := []string{}

	for _, pattern := range portPatterns {
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

	if !isAllowedPort(cfg.Port) {
		return fmt.Errorf("invalid serial port: %q", cfg.Port)
	}

	// Raw fd in non-blocking mode; readLoop waits with poll(2). The fd is
	// owned (and closed) by readLoop.
	fd, err := unix.Open(cfg.Port, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", cfg.Port, err)
	}

	if err := configurePort(fd, cfg); err != nil {
		unix.Close(fd)
		return err
	}

	r.fd = fd
	r.config = cfg
	r.running = true
	r.lines = r.lines[:0]

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.readLoop(ctx, fd)

	return nil
}

// isAllowedPort accepts only the device paths ListPorts can return.
func isAllowedPort(port string) bool {
	for _, pattern := range portPatterns {
		if ok, _ := filepath.Match(pattern, port); ok {
			return true
		}
	}
	return false
}

// Write sends data to the open port.
func (r *Reader) Write(data []byte) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if !r.running || r.fd < 0 {
		return fmt.Errorf("port not open")
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(data) > 0 {
		n, err := unix.Write(r.fd, data)
		if err == unix.EAGAIN || err == unix.EINTR {
			if time.Now().After(deadline) {
				return fmt.Errorf("write timeout")
			}
			pfd := []unix.PollFd{{Fd: int32(r.fd), Events: unix.POLLOUT}}
			unix.Poll(pfd, 100)
			continue
		}
		if err != nil {
			return fmt.Errorf("write: %w", err)
		}
		data = data[n:]
	}
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
		r.cancel = nil
	}
	// readLoop closes the fd once it observes the cancellation
	r.fd = -1
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

func (r *Reader) readLoop(ctx context.Context, fd int) {
	defer unix.Close(fd)

	buf := make([]byte, 4096)
	var lineBuf strings.Builder
	pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}

	for {
		if ctx.Err() != nil {
			return
		}

		pfd[0].Revents = 0
		n, err := unix.Poll(pfd, 100)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			r.readFailed(ctx, err)
			return
		}
		if n == 0 {
			continue
		}
		if pfd[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 && pfd[0].Revents&unix.POLLIN == 0 {
			r.readFailed(ctx, fmt.Errorf("device disconnected"))
			return
		}

		n, err = unix.Read(fd, buf)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EINTR {
				continue
			}
			r.readFailed(ctx, err)
			return
		}
		if n == 0 {
			continue
		}

		// Split incoming bytes into lines
		for i := 0; i < n; i++ {
			b := buf[i]
			switch {
			case b == '\n':
				r.emitLine(lineBuf.String())
				lineBuf.Reset()
			case b == '\r':
			default:
				lineBuf.WriteByte(b)
				if lineBuf.Len() >= maxLineLen {
					r.emitLine(lineBuf.String())
					lineBuf.Reset()
				}
			}
		}
	}
}

// readFailed marks the port closed after an unrecoverable read error (for
// example a USB adapter being unplugged).
func (r *Reader) readFailed(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	r.emitLine(fmt.Sprintf("[serial error: %v, port closed]", err))
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() == nil {
		r.running = false
		r.fd = -1
		if r.cancel != nil {
			r.cancel()
			r.cancel = nil
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

	// Baud rate. TCSETS takes the speed from the CBAUD bits of c_cflag;
	// c_ispeed/c_ospeed are only honoured by TCSETS2.
	speed, ok := baudToSpeed(cfg.BaudRate)
	if !ok {
		return fmt.Errorf("unsupported baud rate: %d", cfg.BaudRate)
	}
	termios.Cflag &^= unix.CBAUD | unix.CBAUDEX
	termios.Cflag |= speed
	termios.Ispeed = speed
	termios.Ospeed = speed

	// Non-blocking reads driven by poll(2)
	termios.Cc[unix.VMIN] = 0
	termios.Cc[unix.VTIME] = 0

	if _, _, errno := unix.Syscall6(unix.SYS_IOCTL, uintptr(fd),
		uintptr(unix.TCSETS), uintptr(unsafe.Pointer(&termios)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("TCSETS: %w", errno)
	}

	return nil
}

var baudRates = map[int]uint32{
	1200:    unix.B1200,
	2400:    unix.B2400,
	4800:    unix.B4800,
	9600:    unix.B9600,
	19200:   unix.B19200,
	38400:   unix.B38400,
	57600:   unix.B57600,
	115200:  unix.B115200,
	230400:  unix.B230400,
	460800:  unix.B460800,
	500000:  unix.B500000,
	576000:  unix.B576000,
	921600:  unix.B921600,
	1000000: unix.B1000000,
	1152000: unix.B1152000,
	1500000: unix.B1500000,
	2000000: unix.B2000000,
	2500000: unix.B2500000,
	3000000: unix.B3000000,
	4000000: unix.B4000000,
}

func baudToSpeed(baud int) (uint32, bool) {
	speed, ok := baudRates[baud]
	return speed, ok
}
