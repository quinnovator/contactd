package mods

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/quinnovator/contactd/internal/tui/conf"
	"github.com/quinnovator/contactd/internal/tui/toml"
)

// Install copies a mod from a directory into the mods directory: its
// mod.toml and the module the manifest names, and nothing else. It records
// where the mod came from and the module's hash. The mod starts off, and a
// mod of the same name that was installed before is replaced and switched
// off: what a mod may read is on the screen before anyone switches it on.
func (h *Host) Install(ctx context.Context, dir string) (Info, error) {
	h.admin.Lock()
	defer h.admin.Unlock()
	if h.isClosed() {
		return Info{}, errClosed
	}
	if h.o.Dir == "" {
		return Info{}, errors.New("install: there is no mods directory")
	}
	src, err := filepath.Abs(dir)
	if err != nil {
		return Info{}, fmt.Errorf("install: %w", err)
	}
	p, err := h.readPackage(src)
	if err != nil {
		return Info{}, fmt.Errorf("install: %w", err)
	}
	name := p.man.Name
	if sameDir(src, filepath.Join(h.o.Dir, name)) {
		return Info{}, fmt.Errorf("install: %s is where the mod is installed already", src)
	}

	h.mu.Lock()
	m := h.mods[name]
	if m == nil {
		m = &mod{name: name}
		h.mods[name] = m
	}
	h.mu.Unlock()

	m.call.Lock()
	defer m.call.Unlock()
	h.unload(ctx, m)
	if err := h.place(p); err != nil {
		// The files may be half replaced: say so and leave the mod off.
		h.fail(m, Manifest{Name: name}, err)
		return h.info(m), fmt.Errorf("install: %w", err)
	}
	return h.settleOff(m, p, entry{source: src, sha256: p.sum}, "")
}

// Update copies a mod again from the directory it was installed from. When
// the module or the manifest changed, the mod is off until it is switched
// on again; when nothing changed it carries on as it was.
func (h *Host) Update(ctx context.Context, name string) (Info, error) {
	h.admin.Lock()
	defer h.admin.Unlock()
	if h.isClosed() {
		return Info{}, errClosed
	}
	m := h.lookup(name)
	if m == nil {
		return Info{}, fmt.Errorf("no mod named %q", clip(name))
	}
	h.mu.Lock()
	e := h.state[name]
	h.mu.Unlock()
	if e.source == "" {
		return h.info(m), fmt.Errorf("update: mod %s was not installed from a directory", name)
	}
	p, err := h.readPackage(e.source)
	if err != nil {
		return h.info(m), fmt.Errorf("update: %w", err)
	}
	if p.man.Name != name {
		return h.info(m), fmt.Errorf("update: %s now holds a mod named %q", e.source, p.man.Name)
	}

	m.call.Lock()
	defer m.call.Unlock()
	old, _ := readRegular(filepath.Join(h.o.Dir, name, ManifestFile), toml.MaxSize)
	if p.sum == e.sha256 && bytes.Equal(old, p.manifest) && h.moduleIs(name, p) {
		return h.info(m), nil
	}
	wasOn := h.info(m).Status == StatusOn
	h.unload(ctx, m)
	if err := h.place(p); err != nil {
		h.fail(m, Manifest{Name: name}, err)
		return h.info(m), fmt.Errorf("update: %w", err)
	}
	why := ""
	if wasOn {
		why = "updated: switch it on to use the new version"
	}
	return h.settleOff(m, p, entry{source: e.source, sha256: p.sum}, why)
}

// pack is a mod read from a directory, checked and ready to copy.
type pack struct {
	man      Manifest
	manifest []byte // mod.toml as written
	module   []byte
	sum      string
}

// readPackage reads and checks a mod in a directory outside the mods
// directory. The directory may have any name.
func (h *Host) readPackage(dir string) (pack, error) {
	manifest, err := readRegular(filepath.Join(dir, ManifestFile), toml.MaxSize)
	if err != nil {
		return pack{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	man, err := parseManifest(manifest)
	if err != nil {
		return pack{}, err
	}
	if err := man.checkBudget(h.o.Budget); err != nil {
		return pack{}, err
	}
	module, err := readRegular(filepath.Join(dir, man.Module), MaxModule)
	if err != nil {
		return pack{}, fmt.Errorf("module: %w", err)
	}
	sum := hashOf(module)
	if man.SHA256 != "" && man.SHA256 != sum {
		return pack{}, errors.New("the module does not match the sha256 in " + ManifestFile)
	}
	return pack{man: man, manifest: manifest, module: module, sum: sum}, nil
}

// moduleIs reports whether the installed module is the one in the package.
func (h *Host) moduleIs(name string, p pack) bool {
	have, err := readRegular(filepath.Join(h.o.Dir, name, p.man.Module), MaxModule)
	return err == nil && hashOf(have) == p.sum
}

// place writes a package into the mods directory. A module left over from a
// version that named its file differently is removed.
func (h *Host) place(p pack) error {
	dest := filepath.Join(h.o.Dir, p.man.Name)
	if old, err := h.readManifest(p.man.Name); err == nil && old.Module != p.man.Module {
		_ = os.Remove(filepath.Join(dest, old.Module))
	}
	if err := conf.WriteFile(filepath.Join(dest, p.man.Module), p.module); err != nil {
		return fmt.Errorf("copy the module: %w", err)
	}
	if err := conf.WriteFile(filepath.Join(dest, ManifestFile), p.manifest); err != nil {
		return fmt.Errorf("copy %s: %w", ManifestFile, err)
	}
	return nil
}

// settleOff records a mod whose files were just written: off, with the new
// hash. m.call is held.
func (h *Host) settleOff(m *mod, p pack, e entry, why string) (Info, error) {
	h.mu.Lock()
	h.state[m.name] = e
	m.info = Info{Manifest: p.man, Status: StatusOff, Reason: why}
	m.times = nil
	err := saveState(h.o.StatePath, h.state)
	h.mu.Unlock()
	return h.info(m), err
}

// sameDir reports whether two paths name one directory.
func sameDir(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	return err == nil && os.SameFile(ia, ib)
}
