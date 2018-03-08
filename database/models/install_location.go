package models

import (
	"path/filepath"

	"github.com/jinzhu/gorm"
)

type InstallLocation struct {
	ID string `json:"id" gorm:"primary_key"`

	Path string `json:"path"`

	Caves []*Cave `json:"caves"`
}

func InstallLocationByID(db *gorm.DB, id string) *InstallLocation {
	var il InstallLocation
	req := db.Where("id = ?", id).First(&il)
	if req.Error != nil {
		if req.RecordNotFound() {
			return nil
		}
		panic(req.Error)
	}
	return &il
}

func (il *InstallLocation) GetInstallFolder(folderName string) string {
	return filepath.Join(il.Path, folderName)
}

func (il *InstallLocation) GetCaves(db *gorm.DB) []*Cave {
	MustPreloadSimple(db, il, "Caves")
	return il.Caves
}
