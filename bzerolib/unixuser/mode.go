package unixuser

import (
	"io/fs"
)

// FileInfo mode comes in format "drwxrwxrwx" always:
// https://cs.opensource.google/go/go/+/refs/tags/go1.18.2:src/io/fs/fs.go;drc=2580d0e08d5e9f979b943758d3c49877fb2324cb;bpv=1;bpt=1;l=194?gsn=String&gs=kythe%3A%2F%2Fgo.googlesource.com%2Fgo%3Flang%3Dgo%3Fpath%3Dio%2Ffs%23method%2520FileMode.String

const (
	readBitOffset    = 0
	writeBitOffset   = 1
	executeBitOffset = 2
)

type userGroup int

const (
	owner userGroup = 1
	group userGroup = 4
	other userGroup = 7
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

	switch mode {
	case read:
		if m.canRead(usrGroup) {
			return true
		}
	case write:
		if m.canWrite(usrGroup) {
			return true
		}
	case execute:
		if m.canExecute(usrGroup) {
			return true
		}
	case open:
		if m.canExecute(usrGroup) {
			return true
		}
	case create:
		if m.canExecute(usrGroup) && m.canWrite(usrGroup) {
			return true
		}
	}
	return false
}

func (m *modeParser) canRead(usr userGroup) bool {
	return string(m.mode[int(usr)+readBitOffset]) == "r"
}

func (m *modeParser) canWrite(usr userGroup) bool {
	return string(m.mode[int(usr)+writeBitOffset]) == "w"
}

func (m *modeParser) canExecute(usr userGroup) bool {
	return string(m.mode[int(usr)+executeBitOffset]) == "x"
}
