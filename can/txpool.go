//go:build linux

package can

import (
	"fmt"
	"sync"
)

// TxPool hands out one transmit-only Bus per interface and transparently
// reopens it after the interface was restarted or reconfigured. It is safe
// for concurrent use.
type TxPool struct {
	mu    sync.Mutex
	buses map[string]*Bus
}

// NewTxPool returns an empty pool.
func NewTxPool() *TxPool {
	return &TxPool{buses: make(map[string]*Bus)}
}

// Bus returns a running TX bus for iface, opening it if needed.
func (p *TxPool) Bus(iface string) (*Bus, error) {
	if iface == "" {
		return nil, fmt.Errorf("interface name is empty")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if b, ok := p.buses[iface]; ok {
		if b.IsRunning() {
			return b, nil
		}
		delete(p.buses, iface)
	}

	b, err := NewBusWithOptions(iface, Options{TxOnly: true})
	if err != nil {
		return nil, err
	}
	// Callers pace their own traffic (replay timing, periodic intervals);
	// the bus only retries while the device TX queue is full.
	b.SetMinSendInterval(0)
	p.buses[iface] = b
	return b, nil
}

// Send queues msg on iface, blocking while the send queue is full. A CAN-FD
// message with at most 8 data bytes and no BRS/ESI is sent as a classic
// frame when the interface is not in CAN-FD mode.
func (p *TxPool) Send(iface string, msg *Message) error {
	b, err := p.Bus(iface)
	if err != nil {
		return err
	}
	if msg.FD && !b.IsFD() {
		if msg.Length > 8 || msg.Flags&(CANFD_BRS|CANFD_ESI) != 0 {
			return fmt.Errorf("%s is not in CAN-FD mode, cannot send %d-byte/BRS frame", iface, msg.Length)
		}
		c := *msg
		c.FD = false
		c.Flags = 0
		msg = &c
	}
	return b.Send(msg)
}

// IsFD reports whether iface is in CAN-FD mode (opening it if needed).
func (p *TxPool) IsFD(iface string) (bool, error) {
	b, err := p.Bus(iface)
	if err != nil {
		return false, err
	}
	return b.IsFD(), nil
}

// Close shuts down all buses.
func (p *TxPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, b := range p.buses {
		b.Shutdown()
		delete(p.buses, name)
	}
}
