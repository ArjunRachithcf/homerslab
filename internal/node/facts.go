// Package node describes a node: the facts its agent reports about the machine, read from the host's own files.
package node

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Facts are what a node's agent reports about its machine. The manager, Admin, the CLI and first-time setup all show
// these, as the agent sent them.
type Facts struct {
	Hostname     string `json:"hostname"`
	Distro       Distro `json:"distro"`
	Architecture string `json:"architecture"` // x86_64 or arm64
	MemoryBytes  uint64 `json:"memoryBytes"`
	Disks        []Disk `json:"disks"`
	TPM          TPM    `json:"tpm"`
	GPUs         []GPU  `json:"gpus"`
}

// Distro is the Linux distribution, from os-release.
type Distro struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Pretty  string `json:"pretty"`
}

// Disk is one physical disk; partitions, loop devices and device-mapper volumes are not disks.
type Disk struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	SizeBytes  uint64 `json:"sizeBytes"`
	Rotational bool   `json:"rotational"`
	Removable  bool   `json:"removable"`
}

// TPM says whether the machine has a TPM, and which version.
type TPM struct {
	Present bool   `json:"present"`
	Version string `json:"version,omitempty"`
}

// GPU is one display controller on the PCI bus.
type GPU struct {
	Vendor string `json:"vendor"`
	Model  string `json:"model"`
	PCIID  string `json:"pciId"`
}

// Read gathers the facts from a system root: "/" on a real machine, a fixture in tests.
func Read(root fs.FS) (Facts, error) {
	var f Facts
	var err error
	if f.Distro, err = distro(root); err != nil {
		return f, err
	}
	if f.MemoryBytes, err = memory(root); err != nil {
		return f, err
	}
	if f.Disks, err = disks(root); err != nil {
		return f, err
	}
	if f.GPUs, err = gpus(root); err != nil {
		return f, err
	}
	f.Hostname = text(root, "proc/sys/kernel/hostname")
	f.Architecture = architecture(text(root, "proc/sys/kernel/arch"))
	f.TPM = tpm(root)
	return f, nil
}

// text is a file's content without surrounding space, or "" when it can't be read.
func text(root fs.FS, name string) string {
	b, err := fs.ReadFile(root, name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func exists(root fs.FS, name string) bool {
	_, err := fs.Stat(root, name)
	return err == nil
}

func distro(root fs.FS) (Distro, error) {
	b, err := fs.ReadFile(root, "etc/os-release")
	if errors.Is(err, fs.ErrNotExist) {
		b, err = fs.ReadFile(root, "usr/lib/os-release")
	}
	if err != nil {
		return Distro{}, fmt.Errorf("reading os-release: %w", err)
	}
	kv := map[string]string{}
	for line := range strings.SplitSeq(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		if u, err := strconv.Unquote(v); err == nil {
			v = u
		} else {
			v = strings.Trim(v, `'"`)
		}
		kv[k] = v
	}
	return Distro{ID: kv["ID"], Name: kv["NAME"], Version: kv["VERSION_ID"], Pretty: kv["PRETTY_NAME"]}, nil
}

func memory(root fs.FS) (uint64, error) {
	b, err := fs.ReadFile(root, "proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("reading meminfo: %w", err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("meminfo's MemTotal: %w", err)
			}
			return kb * 1024, nil
		}
	}
	return 0, errors.New("meminfo has no MemTotal")
}

// architecture names the machine's architecture as HomersLab does: x86_64 or arm64.
func architecture(kernel string) string {
	if kernel == "" {
		kernel = map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	}
	if kernel == "aarch64" {
		return "arm64"
	}
	return kernel
}

// disks lists the block devices that have a device behind them, so loop, zram and device-mapper volumes are left out.
func disks(root fs.FS) ([]Disk, error) {
	entries, err := fs.ReadDir(root, "sys/block")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("listing disks: %w", err)
	}
	out := []Disk{}
	for _, e := range entries {
		dir := path.Join("sys/block", e.Name())
		if !exists(root, path.Join(dir, "device")) {
			continue
		}
		sectors, _ := strconv.ParseUint(text(root, path.Join(dir, "size")), 10, 64)
		model := text(root, path.Join(dir, "device/model"))
		if model == "" {
			model = text(root, path.Join(dir, "device/name")) // SD and eMMC cards
		}
		out = append(out, Disk{
			Name:       e.Name(),
			Model:      model,
			SizeBytes:  sectors * 512, // sysfs counts 512-byte sectors, whatever the disk's own sector size
			Rotational: text(root, path.Join(dir, "queue/rotational")) == "1",
			Removable:  text(root, path.Join(dir, "removable")) == "1",
		})
	}
	return out, nil
}

// ReadTPM is the machine's TPM, from a system root.
func ReadTPM(root fs.FS) TPM { return tpm(root) }

func tpm(root fs.FS) TPM {
	if !exists(root, "sys/class/tpm/tpm0") {
		return TPM{}
	}
	t := TPM{Present: true}
	switch text(root, "sys/class/tpm/tpm0/tpm_version_major") {
	case "2":
		t.Version = "2.0"
	case "1":
		t.Version = "1.2"
	}
	return t
}

// vendors are the short names HomersLab uses for the GPU makers it supports.
var vendors = map[string]string{"10de": "NVIDIA", "1002": "AMD", "8086": "Intel"}

// gpus lists the PCI display controllers (class 0x03), named from the system's pci.ids when it has one.
func gpus(root fs.FS) ([]GPU, error) {
	entries, err := fs.ReadDir(root, "sys/bus/pci/devices")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("listing PCI devices: %w", err)
	}
	out := []GPU{}
	for _, e := range entries {
		dir := path.Join("sys/bus/pci/devices", e.Name())
		if !strings.HasPrefix(text(root, path.Join(dir, "class")), "0x03") {
			continue
		}
		vendor := strings.TrimPrefix(text(root, path.Join(dir, "vendor")), "0x")
		device := strings.TrimPrefix(text(root, path.Join(dir, "device")), "0x")
		out = append(out, GPU{Vendor: vendor, PCIID: vendor + ":" + device})
	}
	if len(out) == 0 {
		return out, nil
	}
	wanted := map[string]bool{}
	for _, g := range out {
		wanted[g.Vendor], wanted[g.PCIID] = true, true
	}
	names := pciNames(root, wanted)
	for i, g := range out {
		out[i].Model = names[g.PCIID]
		if out[i].Vendor = vendors[g.Vendor]; out[i].Vendor == "" {
			out[i].Vendor = names[g.Vendor]
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PCIID < out[j].PCIID })
	return out, nil
}

// pciNames looks up, in the system's pci.ids, the names of the wanted "vendor" and "vendor:device" ids: a vendor's
// name, and a device's marketing name (the part in brackets, when there is one). It stops once it has them all.
func pciNames(root fs.FS, wanted map[string]bool) map[string]string {
	names := map[string]string{}
	var f fs.File
	for _, p := range []string{"usr/share/hwdata/pci.ids", "usr/share/misc/pci.ids"} {
		var err error
		if f, err = root.Open(p); err == nil {
			break
		}
	}
	if f == nil {
		return names
	}
	defer f.Close()
	vendor := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() && len(names) < len(wanted) {
		line := sc.Text()
		switch {
		case line == "" || line[0] == '#' || strings.HasPrefix(line, "\t\t"):
		case line[0] == '\t':
			if id, name, ok := strings.Cut(strings.TrimPrefix(line, "\t"), "  "); ok && vendor != "" && wanted[vendor+":"+id] {
				names[vendor+":"+id] = bracketed(name)
			}
		case strings.HasPrefix(line, "C "):
			return names // the device classes start here
		default:
			id, name, ok := strings.Cut(line, "  ")
			if !ok {
				continue
			}
			vendor = id
			if wanted[id] {
				names[id] = name
			}
		}
	}
	return names
}

func bracketed(name string) string {
	if i, j := strings.Index(name, "["), strings.LastIndex(name, "]"); i >= 0 && j > i {
		return name[i+1 : j]
	}
	return name
}
