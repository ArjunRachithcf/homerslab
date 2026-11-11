package cli

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/node"
)

func showNode(ctx context.Context, args []string, env Env) error {
	fs := flags("node", env)
	socket := fs.String("control-socket", DefaultControlSocket, "the manager's control socket")
	asJSON := fs.Bool("json", false, "print the API's resource as it is")
	if err := parse(fs, args); err != nil {
		return err
	}
	var f node.Facts
	body, err := newControl(*socket).do(ctx, "GET", "/api/v1/nodes/primary", nil, 0, &f)
	if err != nil {
		return err
	}
	if *asJSON {
		_, err := env.Stdout.Write(body)
		return err
	}
	fmt.Fprintf(env.Stdout, "%s, the primary\n", f.Hostname)
	row := func(k, v string) { fmt.Fprintf(env.Stdout, "  %-13s %s\n", k, v) }
	row("Distro", f.Distro.Pretty)
	row("Architecture", f.Architecture)
	row("Memory", Memory(f.MemoryBytes))
	var disks []string
	for _, d := range f.Disks {
		s := fmt.Sprintf("%s, %s", d.Model, DiskSize(d.SizeBytes))
		if d.Rotational {
			s += ", spinning"
		}
		if d.Removable {
			s += ", removable"
		}
		disks = append(disks, s)
	}
	row("Disks", orNone(strings.Join(disks, "; ")))
	tpm := "none"
	if f.TPM.Present {
		tpm = strings.TrimSpace("TPM " + f.TPM.Version)
	}
	row("TPM", tpm)
	var gpus []string
	for _, g := range f.GPUs {
		gpus = append(gpus, strings.TrimSpace(g.Vendor+" "+g.Model))
	}
	row("GPU", orNone(strings.Join(gpus, "; ")))
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Memory is installed memory as people know it: the kernel's total, which is a little under what's fitted, rounded
// up to whole gigabytes (GiB).
func Memory(b uint64) string {
	return fmt.Sprintf("%.0f GB", math.Ceil(float64(b)/(1<<30)))
}

// DiskSize is a disk's size as its maker sells it, in decimal units.
func DiskSize(b uint64) string {
	for _, u := range []struct {
		size float64
		name string
	}{{1e12, "TB"}, {1e9, "GB"}, {1e6, "MB"}} {
		if v := float64(b) / u.size; v >= 1 {
			return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", v), "0"), ".") + " " + u.name
		}
	}
	return fmt.Sprintf("%d bytes", b)
}
