//go:build linux

package can

import (
	"encoding/binary"
	"fmt"
)

const (
	// Classic CAN MTU size (struct can_frame)
	CAN_MTU = 16
	// CAN-FD MTU size (struct canfd_frame)
	CANFD_MTU = 72

	// CAN frame format flags
	CAN_EFF_FLAG = 0x80000000 // Extended Frame Format (EFF)
	CAN_RTR_FLAG = 0x40000000 // Remote Transmission Request
	CAN_ERR_FLAG = 0x20000000 // Error frame

	// CAN-FD specific flags
	CANFD_BRS = 0x01 // Bit Rate Switch (data phase sent at dbitrate)
	CANFD_ESI = 0x02 // Error State Indicator
	CANFD_FDF = 0x04 // FD Frame (set by Linux >= 5.19 on received CAN-FD frames)

	// CAN ID masks
	CAN_SFF_MASK = 0x000007FF // Standard Frame Format mask
	CAN_EFF_MASK = 0x1FFFFFFF // Extended Frame Format mask
)

// Message represents a CAN-FD message
type Message struct {
	ID     uint32   // CAN ID (11-bit for SFF, 29-bit for EFF)
	Length uint8    // Actual data length (0-64 bytes for CAN-FD)
	DLC    uint8    // Original DLC value from SocketCAN frame
	Flags  uint8    // CAN-FD specific flags (BRS, ESI)
	FD     bool     // true for a CAN-FD frame, false for a classic CAN 2.0 frame
	Data   [64]byte // Data payload (max 64 bytes for CAN-FD)
}

// NewMessage creates a new CAN-FD message (without BRS)
func NewMessage(id uint32, data []byte) *Message {
	length := uint8(len(data))
	msg := &Message{
		ID:     id,
		Length: length,
		DLC:    canfdLenToDLC(length),
		FD:     true,
	}

	if len(data) > 64 {
		panic("CAN-FD data length cannot exceed 64 bytes")
	}

	copy(msg.Data[:], data)
	return msg
}

// NewFDMessage creates a new CAN-FD message, optionally with Bit Rate Switch
// so the data phase is transmitted at the interface's data bitrate (dbitrate).
func NewFDMessage(id uint32, data []byte, brs bool) *Message {
	msg := NewMessage(id, data)
	msg.SetBRS(brs)
	return msg
}

// NewClassicMessage creates a new classic CAN 2.0 message (max 8 bytes)
func NewClassicMessage(id uint32, data []byte) *Message {
	if len(data) > 8 {
		panic("classic CAN data length cannot exceed 8 bytes")
	}
	msg := NewMessage(id, data)
	msg.FD = false
	return msg
}

// NewExtendedMessage creates a new extended CAN message (29-bit ID)
func NewExtendedMessage(id uint32, data []byte) *Message {
	msg := NewMessage(id, data)
	msg.ID |= CAN_EFF_FLAG
	return msg
}

// IsExtended returns true if this is an extended frame (29-bit ID)
func (m *Message) IsExtended() bool {
	return (m.ID & CAN_EFF_FLAG) != 0
}

// IsRTR returns true if this is a Remote Transmission Request
func (m *Message) IsRTR() bool {
	return (m.ID & CAN_RTR_FLAG) != 0
}

// IsErrorFrame returns true if this is an error frame
func (m *Message) IsErrorFrame() bool {
	return (m.ID & CAN_ERR_FLAG) != 0
}

// GetActualID returns the actual CAN ID without flags
func (m *Message) GetActualID() uint32 {
	if m.IsExtended() {
		return m.ID & CAN_EFF_MASK
	}
	return m.ID & CAN_SFF_MASK
}

// SetBRS sets the Bit Rate Switch flag for CAN-FD
func (m *Message) SetBRS(enable bool) {
	if enable {
		m.Flags |= CANFD_BRS
	} else {
		m.Flags &= ^uint8(CANFD_BRS)
	}
}

// HasBRS returns true if Bit Rate Switch is enabled
func (m *Message) HasBRS() bool {
	return (m.Flags & CANFD_BRS) != 0
}

// SetESI sets the Error State Indicator flag for CAN-FD
func (m *Message) SetESI(enable bool) {
	if enable {
		m.Flags |= CANFD_ESI
	} else {
		m.Flags &= ^uint8(CANFD_ESI)
	}
}

// HasESI returns true if Error State Indicator is set
func (m *Message) HasESI() bool {
	return (m.Flags & CANFD_ESI) != 0
}

// GetData returns the actual data bytes (up to Length)
func (m *Message) GetData() []byte {
	if m.Length > 64 {
		m.Length = 64
	}
	return m.Data[:m.Length]
}

// Encode writes the Message into frame using the SocketCAN layout that matches
// its type and returns the number of bytes to write to the socket:
// CANFD_MTU (72) for CAN-FD frames, CAN_MTU (16) for classic CAN frames.
func (m *Message) Encode(frame *[CANFD_MTU]byte) (int, error) {
	if m.FD {
		if err := m.Marshal(frame); err != nil {
			return 0, err
		}
		return CANFD_MTU, nil
	}

	if m.Length > 8 {
		return 0, fmt.Errorf("invalid classic CAN data length: %d", m.Length)
	}
	if m.Flags&(CANFD_BRS|CANFD_ESI) != 0 {
		return 0, fmt.Errorf("BRS/ESI flags require a CAN-FD frame")
	}

	for i := range frame {
		frame[i] = 0
	}

	// Linux SocketCAN classic frame structure (can_frame):
	// Bytes 0-3: CAN ID, Byte 4: len (0..8), Bytes 5-7: pad/res/len8_dlc
	// Bytes 8-15: Data
	binary.LittleEndian.PutUint32(frame[0:4], m.ID)
	frame[4] = m.Length
	copy(frame[8:8+m.Length], m.Data[:m.Length])
	return CAN_MTU, nil
}

// Marshal converts the Message to raw CAN-FD frame format
func (m *Message) Marshal(frame *[CANFD_MTU]byte) error {
	if m.Length > 64 {
		return fmt.Errorf("invalid data length: %d", m.Length)
	}

	// Clear the frame
	for i := range frame {
		frame[i] = 0
	}

	// Linux SocketCAN CAN-FD frame structure (canfd_frame):
	// Bytes 0-3: CAN ID (32-bit, little-endian)
	// Byte 4: Data length code (DLC) - encoded length
	// Byte 5: Flags (CAN-FD specific flags like BRS, ESI)
	// Bytes 6-7: Reserved (must be zero)
	// Bytes 8-71: Data (up to 64 bytes)

	binary.LittleEndian.PutUint32(frame[0:4], m.ID)
	// SocketCAN CAN-FD uses actual payload length (0..64), not DLC
	frame[4] = m.Length
	// Only BRS/ESI are meaningful on TX; the kernel sets FDF itself
	frame[5] = m.Flags & (CANFD_BRS | CANFD_ESI)
	// Bytes 6-7 are reserved and already zeroed

	// Copy data starting at byte 8
	copy(frame[8:8+m.Length], m.Data[:m.Length])

	return nil
}

// canfdLenToDLC converts data length to CAN-FD Data Length Code
func canfdLenToDLC(length uint8) uint8 {
	if length <= 8 {
		return length
	}
	switch {
	case length <= 12:
		return 9
	case length <= 16:
		return 10
	case length <= 20:
		return 11
	case length <= 24:
		return 12
	case length <= 32:
		return 13
	case length <= 48:
		return 14
	case length <= 64:
		return 15
	default:
		return 15 // Max DLC
	}
}

// dlcToCanfdLen converts CAN-FD Data Length Code to actual data length
func dlcToCanfdLen(dlc uint8) uint8 {
	if dlc <= 8 {
		return dlc
	}
	switch dlc {
	case 9:
		return 12
	case 10:
		return 16
	case 11:
		return 20
	case 12:
		return 24
	case 13:
		return 32
	case 14:
		return 48
	case 15:
		return 64
	default:
		return 8 // Fallback to classic CAN
	}
}

// Unmarshal parses a raw CAN-FD frame into a Message
func (m *Message) Unmarshal(frame []byte) error {
	if len(frame) < 8 {
		return fmt.Errorf("frame too short: %d bytes", len(frame))
	}

	// Parse CAN-FD frame structure
	m.ID = binary.LittleEndian.Uint32(frame[0:4])
	// SocketCAN: byte 4 is actual length (0..64), not DLC
	length := frame[4]
	m.Length = length
	m.DLC = canfdLenToDLC(length)
	// A read returns CANFD_MTU bytes for CAN-FD frames and CAN_MTU bytes for
	// classic frames. Byte 5 is only the flags field (BRS/ESI/FDF) for CAN-FD;
	// for classic frames it is padding and must not be read as flags.
	m.FD = len(frame) == CANFD_MTU
	if m.FD {
		m.Flags = frame[5]
	} else {
		m.Flags = 0
	}
	// Bytes 6-7 are reserved

	if m.Length > 64 {
		return fmt.Errorf("invalid data length: %d", m.Length)
	}

	// Clear data array
	for i := range m.Data {
		m.Data[i] = 0
	}

	if !m.FD && m.Length > 8 {
		return fmt.Errorf("invalid classic CAN data length: %d", m.Length)
	}
	if len(frame) < 8+int(m.Length) {
		return fmt.Errorf("frame truncated: %d bytes for data length %d", len(frame), m.Length)
	}
	copy(m.Data[:m.Length], frame[8:8+m.Length])

	return nil
}

// String returns a human-readable representation of the message
func (m *Message) String() string {
	frameType := "SFF"
	if m.IsExtended() {
		frameType = "EFF"
	}
	proto := "CAN"
	if m.FD {
		proto = "CAN-FD"
	}

	flags := ""
	if m.HasBRS() {
		flags += " BRS"
	}
	if m.HasESI() {
		flags += " ESI"
	}
	if m.IsRTR() {
		flags += " RTR"
	}
	if m.IsErrorFrame() {
		flags += " ERR"
	}

	return fmt.Sprintf("%s[%s] ID:0x%X Len:%d%s Data:%X",
		proto, frameType, m.GetActualID(), m.Length, flags, m.GetData())
}
