/*
The golang standard libaray provides us with access to the file permission bits, but not a great way
of assessing permission based on those bits. The ModeParser struct takes in a file mode and converts
it to a string which always has the format "_rwxrwxrwx", where the first bit is in the set
{dalTLDpSugct}.

Permissions bits are segmented to describe the permissions of 3 different groups: user, group, and other
The "user" set is for the owner of the file
The "group" set is for those in the same group as the file
the "other" set is for those that are neither owners nor group members

Each category, which we refer to below as a "Privilege Set", has 3 corresponding permission bits which
are output as "rwx" fs.FileMode.String() is called. If the permission bit is not set, instead of the
appropriate character, there is a "-". That's why, in order to check whether a given privilege set has
read permission, we check whether the bit at the readBitOffset is "r".

Possible permissions checks:
    - read: whether the user can read a file
    - write: whether the user can write to a file
    - execute: whether the user can execute a file

The following two permissions types are abstractions that I've found helpful. They're not explicitly
stated (the only permissions we ever get defined for a given privilege set are the previous 3), but
they might still be interesting for the code to check and understand especially if the code writer
doesn't want to go read a whole bunch on unix file permissions.
    - open: whether the user can open a file (aka execute)
    - create: whether the user can create the given file (write + execute)
*/
package filemode

import (
	"io/fs"
)

const (
	readBitOffset    = 0
	writeBitOffset   = 1
	executeBitOffset = 2
)

type PrivilegeSet int

const (
	User  PrivilegeSet = 1
	Group PrivilegeSet = 4
	Other PrivilegeSet = 7
)

type CheckType string

const (
	Read    CheckType = "read"
	Write   CheckType = "write"
	Execute CheckType = "execute"

	// open and create are helpful abstractions since the only explicit permissions are for read, write,
	// and execute
	Open   CheckType = "open"
	Create CheckType = "create"
	Remove CheckType = "remove"
)

type ModeParser struct {
	mode string
}

func NewParser(mode fs.FileMode) *ModeParser {
	return &ModeParser{
		mode: mode.String(),
	}
}

func (m *ModeParser) Verify(set PrivilegeSet, mode CheckType) bool {
	switch mode {
	case Read:
		return m.CanRead(set)
	case Write:
		return m.CanWrite(set)
	case Execute, Open:
		return m.CanExecute(set)
	case Create, Remove:
		return m.CanCreate(set)
	default:
		return false
	}
}

func (m *ModeParser) CanRead(set PrivilegeSet) bool {
	return string(m.mode[int(set)+readBitOffset]) == "r"
}

func (m *ModeParser) CanWrite(set PrivilegeSet) bool {
	return string(m.mode[int(set)+writeBitOffset]) == "w"
}

func (m *ModeParser) CanExecute(set PrivilegeSet) bool {
	return string(m.mode[int(set)+executeBitOffset]) == "x"
}

func (m *ModeParser) CanOpen(set PrivilegeSet) bool {
	return m.CanExecute(set)
}

func (m *ModeParser) CanCreate(set PrivilegeSet) bool {
	return m.CanExecute(set) && m.CanWrite(set)
}

func (m *ModeParser) CanRemove(set PrivilegeSet) bool {
	return m.CanCreate(set)
}
