package ssh

import (
	"bastionzero.com/bctl/v1/bzerolib/bzio"
)

type IIdentityFile interface {
	SetKey(privateKey []byte) error
	GetKey() ([]byte, error)
}

type IdentityFile struct {
	filePath string
	fileIo   bzio.BzFileIo
}

func NewIdentityFile(filePath string, fileIo bzio.BzFileIo) *IdentityFile {
	return &IdentityFile{
		filePath: filePath,
		fileIo:   fileIo,
	}
}

func (f *IdentityFile) SetKey(privateKey []byte) error {
	return f.fileIo.WriteFile(f.filePath, privateKey, 0600)
}

func (f *IdentityFile) GetKey() ([]byte, error) {
	return f.fileIo.ReadFile(f.filePath)
}
