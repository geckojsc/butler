package installer

import (
	"github.com/itchio/butler/installer/bfs"
	"github.com/itchio/wharf/eos"
	"github.com/itchio/wharf/state"
)

type Manager interface {
	Install(params *InstallParams) (*InstallResult, error)
	Uninstall(params *UninstallParams) error
	Name() string
}

type InstallParams struct {
	// An archive file, .exe setup file, .dmg file etc.
	File eos.File

	// The existing receipt, if any
	ReceiptIn *bfs.Receipt

	// A folder we can use to store temp files
	StageFolderPath string

	// The folder we're installing to
	InstallFolderPath string

	// Listener for progress events, logging etc.
	Consumer *state.Consumer
}

type UninstallParams struct {
	// The folder we're uninstalling from
	InstallFolderPath string
}

type InstallResult struct {
	// Files is a list of paths, relative to the install folder
	Files []string
}
