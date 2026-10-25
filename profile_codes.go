package main

import (
	"fmt"

	"openlistfs/internal/profilecode"
	"openlistfs/internal/profiles"
)

type ProfileCodeResult struct {
	Name  string `json:"name"`
	Code  string `json:"code"`
	Error string `json:"error"`
}

// ExportProfileCode exports a saved profile; legacy profiles include the current
// mount options so that the selected backend/proxy survive transfer.
func (a *App) ExportProfileCode(name string) ProfileCodeResult {
	a.mu.Lock()
	p, ok := a.doc.Profiles[name]
	opts := toStoreOptions(a.opts)
	a.mu.Unlock()
	if !ok {
		return ProfileCodeResult{Error: "请选择已保存的配置后导出。"}
	}
	if p.Options == nil {
		p.Options = &opts
	}
	code, err := profilecode.Encode(name, p)
	if err != nil {
		return ProfileCodeResult{Error: err.Error()}
	}
	return ProfileCodeResult{Name: name, Code: code}
}

func (a *App) ImportProfileCode(code string) ProfileCodeResult {
	c, err := profilecode.Decode(code)
	if err != nil {
		return ProfileCodeResult{Error: err.Error()}
	}
	name, err := a.importProfile(c, profiles.Save)
	if err != nil {
		return ProfileCodeResult{Error: fmt.Sprintf("无法保存导入的配置：%v", err)}
	}
	return ProfileCodeResult{Name: name}
}

// Persist a staged copy before updating the running application's state. A
// failed save leaves the original profiles/options/active selection untouched.
func (a *App) importProfile(c profilecode.Configuration, save func(profiles.Document) error) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	next, name, err := profilecode.Add(a.doc, c)
	if err != nil {
		return "", err
	}
	if err := save(next); err != nil {
		return "", err
	}
	a.doc = next
	if c.Profile.Options != nil {
		a.opts = fromStoreOptions(next.Options)
	}
	return name, nil
}
