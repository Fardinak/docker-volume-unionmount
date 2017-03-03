package main

import (
	"encoding/gob"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/docker/go-plugins-helpers/volume"
)

// TODO: Separate the filesystems into libraries
const (
	fsAUFS = iota
	fsOverlay
)

type unionMountVolume struct {
	Filesystem int
	Layers     []string
	MountPoint string
	RefCount   uint
	m          sync.Mutex
}

type unionMountDriver struct {
	RootDir   string
	DefaultFS int
	Volumes   map[string]*unionMountVolume
	m         sync.Mutex
}

func newUnionMountDriver(rootDir string, defaultFS int) *unionMountDriver {
	return &unionMountDriver{
		RootDir:   rootDir,
		DefaultFS: defaultFS,
		Volumes:   make(map[string]*unionMountVolume),
	}
}

func (d *unionMountDriver) saveState() {
	d.m.Lock()
	defer d.m.Unlock()

	path := filepath.Join(d.RootDir, "state.gob")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		// TODO: Log error
		return
	}
	defer file.Close()

	enc := gob.NewEncoder(file)
	if err := enc.Encode(d); err != nil {
		// TODO: Log error
	}
}

func (d *unionMountDriver) mountPoint(volName string) string {
	return filepath.Join(d.RootDir, "volumes", volName)
}

func (d *unionMountDriver) Create(r volume.Request) volume.Response {
	d.m.Lock()
	defer d.m.Unlock()

	// Try to read the layers option
	layers := make([]string, 0)
	if str, ok := r.Options["layers"]; ok && len(str) > 0 {
		layers = strings.Split(str, ":")

		// Check if paths are absolute
		// REVIEW: the possibility of layering docker's named volumes
		for _, path := range layers {
			if !filepath.IsAbs(path) {
				return volume.Response{Err: fmt.Sprintf("layer path \"%s\" is not relative", path)}
			}
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return volume.Response{Err: fmt.Sprintf("layer path \"%s\" does not exist", path)}
			}
		}
	} else {
		return volume.Response{Err: "no layers defined"}
	}

	// Try to read the filesystem option
	filesystem := d.DefaultFS
	if str, ok := r.Options["filesystem"]; ok {
		if d, err := fsFromString(str); err == nil {
			filesystem = d
		} else {
			return volume.Response{Err: err.Error()}
		}
	}

	// FIXME: Support multiple layers for overlay
	if filesystem == fsOverlay && len(layers) > 1 {
		return volume.Response{Err: "multiple layers with the overlay filesystem is not implemented"}
	}

	// Create Mount Point
	mountPoint := d.mountPoint(r.Name)
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return volume.Response{Err: err.Error()}
	}

	// Check for duplicate volume name
	if _, ok := d.Volumes[r.Name]; ok {
		return volume.Response{Err: fmt.Sprintf("volume \"%s\" already exists", r.Name)}
	}

	d.Volumes[r.Name] = &unionMountVolume{
		Filesystem: filesystem,
		Layers:     layers,
		MountPoint: mountPoint,
	}

	go d.saveState()

	return volume.Response{}
}

func (d *unionMountDriver) Remove(r volume.Request) volume.Response {
	d.m.Lock()
	defer d.m.Unlock()

	// Check if volume exists
	vol, ok := d.Volumes[r.Name]
	if !ok {
		return volume.Response{Err: fmt.Sprintf("volume (%s) does not exist", r.Name)}
	}

	vol.m.Lock()
	defer vol.m.Unlock()

	if vol.RefCount == 0 {
		if err := os.RemoveAll(vol.MountPoint); err != nil {
			return volume.Response{Err: fmt.Sprintf("error removing volume path '%s'", vol.MountPoint)}
		}
		delete(d.Volumes, r.Name)
	}

	go d.saveState()

	return volume.Response{}
}

func (d *unionMountDriver) Mount(r volume.MountRequest) volume.Response {

	// Check if volume exists
	d.m.Lock()
	vol, ok := d.Volumes[r.Name]
	d.m.Unlock()
	if !ok {
		return volume.Response{Err: fmt.Sprintf("volume (%s) does not exist", r.Name)}
	}

	vol.m.Lock()
	defer vol.m.Unlock()

	// Mount Volume if not already mounted
	if vol.RefCount == 0 {
		cmd, _ := mountCmd(vol)
		err := exec.Command("sh", "-c", cmd).Run()
		if err != nil {
			return volume.Response{Err: err.Error()}
		}
	}

	vol.RefCount++

	return volume.Response{Mountpoint: vol.MountPoint}
}

func (d *unionMountDriver) Path(r volume.Request) volume.Response {
	// Check if volume exists
	d.m.Lock()
	vol, ok := d.Volumes[r.Name]
	d.m.Unlock()
	if !ok {
		return volume.Response{Err: fmt.Sprintf("volume (%s) does not exist", r.Name)}
	}

	return volume.Response{Mountpoint: vol.MountPoint}
}

func (d *unionMountDriver) Unmount(r volume.UnmountRequest) volume.Response {
	// Check if volume exists
	d.m.Lock()
	vol, ok := d.Volumes[r.Name]
	d.m.Unlock()
	if !ok {
		return volume.Response{Err: fmt.Sprintf("volume (%s) does not exist", r.Name)}
	}

	vol.m.Lock()
	defer vol.m.Unlock()

	if vol.RefCount == 1 {
		exec.Command("sh", "-c", fmt.Sprintf("umount -f %s", vol.MountPoint)).Run()
	} else if vol.RefCount == 0 {
		return volume.Response{Err: fmt.Sprintf("volume (%s) is not mounted", r.Name)}
	}

	vol.RefCount--

	return volume.Response{}
}

func (d *unionMountDriver) Get(r volume.Request) volume.Response {
	// Check if volume exists
	d.m.Lock()
	vol, ok := d.Volumes[r.Name]
	d.m.Unlock()
	if !ok {
		return volume.Response{Err: fmt.Sprintf("volume (%s) does not exist", r.Name)}
	}

	return volume.Response{
		Volume: &volume.Volume{
			Name:       r.Name,
			Mountpoint: vol.MountPoint,
		},
	}
}

func (d *unionMountDriver) List(r volume.Request) volume.Response {
	d.m.Lock()
	defer d.m.Unlock()

	volumes := []*volume.Volume{}

	for n, v := range d.Volumes {
		volumes = append(volumes, &volume.Volume{
			Name:       n,
			Mountpoint: v.MountPoint,
		})
	}

	return volume.Response{Volumes: volumes}
}

func (d *unionMountDriver) Capabilities(r volume.Request) volume.Response {
	return volume.Response{
		Capabilities: volume.Capability{
			Scope: "local",
		},
	}
}

func fsFromString(fs string) (int, error) {
	switch strings.ToLower(fs) {
	case "aufs":
		return fsAUFS, nil
	case "overlay", "overlayfs":
		return fsOverlay, nil
	default:
		return fsAUFS, fmt.Errorf("unsupported filesystem")
	}
}

func mountCmd(v *unionMountVolume) (string, error) {
	switch v.Filesystem {
	case fsAUFS:
		return fmt.Sprintf("mount -t aufs -o br=%s:%s none %s", v.MountPoint, strings.Join(v.Layers, ":"), v.MountPoint), nil
	case fsOverlay:
		return fmt.Sprintf("mount -t overlay overlay -o lowerdir=%s,upperdir=%s %s", v.Layers[0], v.MountPoint, v.MountPoint), nil
	default:
		return "", fmt.Errorf("undefined or unsupported filesystem")
	}
}
