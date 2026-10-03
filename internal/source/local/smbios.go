package local

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"github.com/frauniki/netbox-groundtruth-agent/internal/snapshot"
)

// smbiosStructure is one structure of the SMBIOS table (DMTF DSP0134 6.1.2).
type smbiosStructure struct {
	Type      byte
	Formatted []byte // the whole formatted area, header included
	Strings   []string
}

// parseSMBIOS splits a raw table as exposed in /sys/firmware/dmi/tables/DMI.
func parseSMBIOS(b []byte) ([]smbiosStructure, error) {
	var out []smbiosStructure
	for len(b) >= 4 {
		typ, length := b[0], int(b[1])
		if length < 4 || length > len(b) {
			return out, fmt.Errorf("smbios: bad structure length %d", length)
		}
		s := smbiosStructure{Type: typ, Formatted: b[:length]}
		// The string set ends with a double NUL; an empty set is just "\0\0".
		end := strings.Index(string(b[length:]), "\x00\x00")
		if end < 0 {
			return out, fmt.Errorf("smbios: unterminated string set")
		}
		if end > 0 {
			s.Strings = strings.Split(string(b[length:length+end]), "\x00")
		}
		out = append(out, s)
		if typ == 127 { // end-of-table
			break
		}
		b = b[length+end+2:]
	}
	return out, nil
}

func (s smbiosStructure) byteAt(off int) (byte, bool) {
	if off >= len(s.Formatted) {
		return 0, false
	}
	return s.Formatted[off], true
}

func (s smbiosStructure) word(off int) (uint16, bool) {
	if off+2 > len(s.Formatted) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(s.Formatted[off:]), true
}

func (s smbiosStructure) dword(off int) (uint32, bool) {
	if off+4 > len(s.Formatted) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(s.Formatted[off:]), true
}

// str returns the string referenced at offset off (1-based index, 0 = none).
func (s smbiosStructure) str(off int) string {
	i, ok := s.byteAt(off)
	if !ok || i == 0 || int(i) > len(s.Strings) {
		return ""
	}
	return clean(s.Strings[i-1])
}

// memoryTypes maps SMBIOS Type 17 Memory Type (offset 12h) to a name.
var memoryTypes = map[byte]string{
	0x12: "DDR", 0x13: "DDR2", 0x14: "DDR2 FB-DIMM", 0x18: "DDR3", 0x19: "FBD2",
	0x1A: "DDR4", 0x1B: "LPDDR", 0x1C: "LPDDR2", 0x1D: "LPDDR3", 0x1E: "LPDDR4",
	0x1F: "Logical non-volatile device", 0x20: "HBM", 0x21: "HBM2", 0x22: "DDR5",
	0x23: "LPDDR5", 0x24: "HBM3",
}

// memoryModule decodes a Type 17 (Memory Device) structure. ok is false for
// empty slots.
func memoryModule(s smbiosStructure) (m snapshot.MemoryModule, ok bool) {
	size, has := s.word(0x0C)
	if !has || size == 0 || size == 0xFFFF {
		return m, false
	}
	switch {
	case size == 0x7FFF:
		ext, has := s.dword(0x1C)
		if !has {
			return m, false
		}
		m.SizeBytes = uint64(ext&0x7FFFFFFF) << 20
	case size&0x8000 != 0:
		m.SizeBytes = uint64(size&0x7FFF) << 10
	default:
		m.SizeBytes = uint64(size) << 20
	}
	m.Locator = s.str(0x10)
	m.BankLocator = s.str(0x11)
	if t, has := s.byteAt(0x12); has {
		m.Type = memoryTypes[t]
	}
	if sp, has := s.word(0x15); has {
		if sp == 0xFFFF {
			if ext, has := s.dword(0x54); has {
				m.SpeedMTs = int(ext & 0x7FFFFFFF)
			}
		} else {
			m.SpeedMTs = int(sp)
		}
	}
	m.Manufacturer = s.str(0x17)
	m.Serial = s.str(0x18)
	m.PartNumber = s.str(0x1A)
	return m, true
}

// placeholders are values firmware uses instead of leaving a field empty.
var placeholders = []string{
	"to be filled by o.e.m.", "not specified", "default string", "system serial number",
	"system product name", "system manufacturer", "not available", "not applicable",
	"none", "n/a", "na", "unknown", "0123456789", "123456789", "0", "00000000",
	"serial number", "sernum0", "chassis serial number", "base board serial number",
	"no dimm", "empty", "undefined",
}

// clean trims value and returns "" for firmware placeholders.
func clean(v string) string {
	v = strings.TrimSpace(v)
	if slices.Contains(placeholders, strings.ToLower(v)) {
		return ""
	}
	return v
}
