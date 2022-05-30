package unixuser

import (
	"io/fs"
)

// FileInfo mode comes in format "drwxrwxrwx" always:
// https://cs.opensource.google/go/go/+/refs/tags/go1.18.2:src/io/fs/fs.go;drc=2580d0e08d5e9f979b943758d3c49877fb2324cb;bpv=1;bpt=1;l=194?gsn=String&gs=kythe%3A%2F%2Fgo.googlesource.com%2Fgo%3Flang%3Dgo%3Fpath%3Dio%2Ffs%23method%2520FileMode.String
const (
	ownerReadBit    = 1
	ownerWriteBit   = 2
	ownerExecuteBit = 3
	groupReadBit    = 4
	groupWriteBit   = 5
	groupExecuteBit = 6
	otherReadBit    = 7
	otherWriteBit   = 8
	otherExecuteBit = 9
)

type modeParser struct {
	mode string
}

func newFileModeParser(mode fs.FileMode) *modeParser {
	return &modeParser{
		mode: mode.String(),
	}
}

func (m *modeParser) verify(usrGroup userGroup, mode checkPermissionMode) bool {
	switch usrGroup {
	case owner:
		switch mode {
		case read:
			if m.ownerCanRead() {
				return true
			}
		case write:
			if m.ownerCanWrite() {
				return true
			}
		case execute:
			if m.ownerCanExecute() {
				return true
			}
		case open:
			if m.ownerCanOpen() {
				return true
			}
		case create:
			if m.ownerCanCreate() {
				return true
			}
		}
	case group:
		switch mode {
		case read:
			if m.groupCanRead() {
				return true
			}
		case write:
			if m.groupCanWrite() {
				return true
			}
		case execute:
			if m.groupCanExecute() {
				return true
			}
		case open:
			if m.groupCanOpen() {
				return true
			}
		case create:
			if m.groupCanCreate() {
				return true
			}
		}
	case other:
		switch mode {
		case read:
			if m.otherCanRead() {
				return true
			}
		case write:
			if m.otherCanWrite() {
				return true
			}
		case execute:
			if m.otherCanExecute() {
				return true
			}
		case open:
			if m.otherCanOpen() {
				return true
			}
		case create:
			if m.otherCanCreate() {
				return true
			}
		}
	}
	return false
}

// functions for defining owner priveledges

func (m *modeParser) ownerCanRead() bool {
	return m.canRead(ownerReadBit)
}

func (m *modeParser) ownerCanWrite() bool {
	return m.canWrite(ownerWriteBit)
}

func (m *modeParser) ownerCanExecute() bool {
	return m.canExecute(ownerExecuteBit)
}

func (m *modeParser) ownerCanOpen() bool {
	return m.ownerCanExecute()
}

func (m *modeParser) ownerCanCreate() bool {
	return m.ownerCanWrite() && m.ownerCanOpen()
}

// functions for defining group priveledges

func (m *modeParser) groupCanRead() bool {
	return m.canRead(groupReadBit)
}

func (m *modeParser) groupCanWrite() bool {
	return m.canWrite(groupWriteBit)
}

func (m *modeParser) groupCanExecute() bool {
	return m.canExecute(groupExecuteBit)
}

func (m *modeParser) groupCanOpen() bool {
	return m.ownerCanExecute()
}

func (m *modeParser) groupCanCreate() bool {
	return m.groupCanWrite() && m.groupCanOpen()
}

// functions for defining other priveledges

func (m *modeParser) otherCanRead() bool {
	return m.canRead(otherReadBit)
}

func (m *modeParser) otherCanWrite() bool {
	return m.canWrite(otherWriteBit)
}

func (m *modeParser) otherCanExecute() bool {
	return m.canExecute(otherExecuteBit)
}

func (m *modeParser) otherCanOpen() bool {
	return m.otherCanExecute()
}

func (m *modeParser) otherCanCreate() bool {
	return m.otherCanWrite() && m.otherCanOpen()
}

// helper functions for checking whether bit is set depending on expected value

func (m *modeParser) canRead(index int) bool {
	return string(m.mode[index]) == "r"
}

func (m *modeParser) canWrite(index int) bool {
	return string(m.mode[index]) == "w"
}

func (m *modeParser) canExecute(index int) bool {
	return string(m.mode[index]) == "x"
}
