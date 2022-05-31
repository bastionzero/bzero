package filemode

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

type UserGroup int

const (
	Owner UserGroup = 1
	Group UserGroup = 4
	Other UserGroup = 7
)

type CheckType string

const (
	Read    CheckType = "read"
	Write   CheckType = "write"
	Execute CheckType = "execute"
	Open    CheckType = "open"
	Create  CheckType = "create"
)

type ModeParser struct {
	mode string
}

func NewParser(mode fs.FileMode) *ModeParser {
	return &ModeParser{
		mode: mode.String(),
	}
}

func (m *ModeParser) Verify(usrGroup UserGroup, mode CheckType) bool {
	switch mode {
	case Read:
		if m.CanRead(usrGroup) {
			return true
		}
	case Write:
		if m.CanWrite(usrGroup) {
			return true
		}
	case Execute:
		if m.CanExecute(usrGroup) {
			return true
		}
	case Open:
		if m.CanOpen(usrGroup) {
			return true
		}
	case Create:
		if m.CanCreate(usrGroup) {
			return true
		}
	}
	return false
}

func (m *ModeParser) CanRead(usr UserGroup) bool {
	return string(m.mode[int(usr)+readBitOffset]) == "r"
}

func (m *ModeParser) CanWrite(usr UserGroup) bool {
	return string(m.mode[int(usr)+writeBitOffset]) == "w"
}

func (m *ModeParser) CanExecute(usr UserGroup) bool {
	return string(m.mode[int(usr)+executeBitOffset]) == "x"
}

func (m *ModeParser) CanOpen(usr UserGroup) bool {
	return m.CanExecute(usr)
}

func (m *ModeParser) CanCreate(usr UserGroup) bool {
	return m.CanExecute(usr) && m.CanWrite(usr)
}
