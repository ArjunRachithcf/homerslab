package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/manager/plan"
)

// leading takes a command's one positional argument, which comes before its flags.
func leading(args []string) (string, []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", args
	}
	return args[0], args[1:]
}

func showCatalog(ctx context.Context, args []string, env Env) error {
	fs := flags("catalog", env)
	socket := fs.String("control-socket", DefaultControlSocket, "the manager's control socket")
	asJSON := fs.Bool("json", false, "print the API's resource as it is")
	if err := parse(fs, args); err != nil {
		return err
	}
	var c struct {
		Catalog    struct{ Name, Publisher string }
		Categories []catalog.Category
		Apps       []struct {
			Name, Purpose, Category string
			Tier                    catalog.Tier
			SignIn                  catalog.SignInMethod `json:"signIn"`
		}
	}
	body, err := newControl(*socket).do(ctx, "GET", "/api/v1/catalog", 0, &c)
	if err != nil {
		return err
	}
	if *asJSON {
		_, err := env.Stdout.Write(body)
		return err
	}
	categories := map[string]string{}
	for _, k := range c.Categories {
		categories[k.ID] = k.Name
	}
	fmt.Fprintf(env.Stdout, "%s, from %s\n", c.Catalog.Name, c.Catalog.Publisher)
	for _, a := range c.Apps {
		how := "native sign-in"
		if a.SignIn == catalog.Guard {
			how = "guarded at the edge"
		}
		fmt.Fprintf(env.Stdout, "  %-10s %-10s %-20s %s, %s\n", a.Name, a.Purpose, categories[a.Category], upperFirst(string(a.Tier)), how)
	}
	fmt.Fprint(env.Stdout, "\nTo see what installing one would do: homerslab plan <app>\n")
	return nil
}

func makePlan(ctx context.Context, args []string, env Env) error {
	app, rest := leading(args)
	fs := flags("plan", env)
	socket := fs.String("control-socket", DefaultControlSocket, "the manager's control socket")
	asJSON := fs.Bool("json", false, "print the API's resource as it is")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: homerslab plan <app> [--json]\n       homerslab plan show <plan id> [--json]\n")
	}
	if err := parse(fs, rest); err != nil {
		return err
	}
	if app == "" {
		fs.Usage()
		return errUsage
	}
	return showPlan(ctx, env, *socket, *asJSON, "POST", "/api/v1/plans", map[string]string{"app": app}, "no app "+app+" in the catalog: homerslab catalog lists them")
}

func showPlanByID(ctx context.Context, args []string, env Env) error {
	id, rest := leading(args)
	fs := flags("plan show", env)
	socket := fs.String("control-socket", DefaultControlSocket, "the manager's control socket")
	asJSON := fs.Bool("json", false, "print the API's resource as it is")
	if err := parse(fs, rest); err != nil {
		return err
	}
	if id == "" {
		fmt.Fprint(fs.Output(), "Usage: homerslab plan show <plan id> [--json]\n")
		return errUsage
	}
	return showPlan(ctx, env, *socket, *asJSON, "GET", "/api/v1/plans/"+id, nil, "no plan "+id)
}

func showPlan(ctx context.Context, env Env, socket string, asJSON bool, method, path string, body any, notFound string) error {
	var p plan.Plan
	var payload *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	raw, err := newControl(socket).request(ctx, method, path, 0, payload, &p)
	var ae *apiError
	if errors.As(err, &ae) {
		switch ae.Code {
		case "no-such-app", "no-such-plan":
			return errors.New(notFound)
		case "household-not-set-up":
			return errors.New("the household isn't set up yet: first-time setup comes first (sudo homerslab setup link)")
		}
	}
	if err != nil {
		return err
	}
	if asJSON {
		_, err := env.Stdout.Write(raw)
		return err
	}
	planWords(env.Stdout, p)
	return nil
}

// planWords says a plan in plain words, as Admin's plan page does.
func planWords(w io.Writer, p plan.Plan) {
	fmt.Fprintf(w, "Install %s %s, from the %s catalog\n", p.App.Name, p.App.Version, p.App.Catalog)
	fmt.Fprintf(w, "%s, published by %s\n", upperFirst(string(p.App.Tier)), p.App.Publisher)
	fmt.Fprintf(w, "Plan %s, made %s\n\n", p.ID, p.MadeAt.Local().Format("2 Jan 2006 15:04"))
	row := func(k, v string) { fmt.Fprintf(w, "  %-14s %s\n", k, v) }
	more := func(v string) { fmt.Fprintf(w, "  %-14s %s\n", "", v) }

	exposure := "Mesh only, always."
	if p.Name.PublicAllowed {
		exposure = "Mesh only: it can be made public later, with your passkey."
	}
	row("Its name", fmt.Sprintf("%s, on %s. %s", p.Name.Host, strings.Join(p.Name.Meshes, " and "), exposure))
	if p.Name.Why != "" {
		more(fmt.Sprintf("It asked for %q, which %s.", p.Name.Asked, map[string]string{
			"platform": "is HomersLab's own: the sign-in, Admin or Home", "mesh": "the mesh uses to check the domain",
			"dns": "starts with _, as DNS's own names do", "web": "is the web's usual name for the domain", "mail": "mail uses",
			"node": "is a node's name", "app": "another app has",
		}[p.Name.Why]))
	}

	var people []string
	for _, x := range p.Audience.People {
		people = append(people, x.Name)
	}
	row("Who sees it", fmt.Sprintf("%s: %s", upperFirst(p.Audience.Default), orNone(strings.Join(people, ", "))))
	if p.Audience.KidView != "" {
		more("Kids get its kid view: " + lowerFirst(p.Audience.KidView))
	}

	switch p.SignIn.Method {
	case catalog.OIDC:
		row("Sign-in", "With the household account, straight into "+p.App.Name+": an OIDC client in "+p.SignIn.Provider.Name)
	case catalog.LDAP:
		row("Sign-in", "With the household account, through LDAP access in "+p.SignIn.Provider.Name)
	default:
		row("Sign-in", fmt.Sprintf("Guarded: the edge on %s asks for the household account before %s sees a request", p.Placement.Node, p.App.Name))
	}

	var data []string
	for _, d := range p.Backups.Data {
		s := lowerFirst(d.What)
		if d.Kind == "dump" {
			s += " (a dump)"
		}
		data = append(data, s)
	}
	if data == nil {
		row("Backups", "Nothing to back up: it keeps no data.")
	} else {
		row("Backups", fmt.Sprintf("Every %s: %s. A restore test every %s.", p.Backups.Every, strings.Join(data, ", "), p.Backups.RestoreTest))
	}
	for _, l := range p.Backups.Left {
		why := l.Why
		if !strings.HasPrefix(why, p.App.Name) {
			why = lowerFirst(why)
		}
		more(fmt.Sprintf("Not %s: %s.", lowerFirst(l.What), why))
	}

	var services []string
	for _, s := range p.Placement.Services {
		services = append(services, s.Name)
	}
	containers := "1 container"
	if len(services) != 1 {
		containers = fmt.Sprintf("%d containers", len(services))
	}
	row("Where it runs", fmt.Sprintf("%s (%s, %s), in %s: %s", p.Placement.Node, p.Placement.Architecture, upperFirst(string(p.Placement.Engine)), containers, strings.Join(services, ", ")))

	if len(p.Extras) == 0 {
		row("Extras", "None")
	}
	for i, x := range p.Extras {
		k := ""
		if i == 0 {
			k = "Extras"
		}
		row(k, fmt.Sprintf("%s (%s %s for %s)", x.Why, x.Kind, x.Device+x.Capability, x.Service))
	}
	if p.Risky {
		more("Your passkey approves this plan's extras, for this plan only.")
	}

	var secrets []string
	for _, s := range p.Secrets {
		secrets = append(secrets, s.What)
	}
	sealed := p.Placement.Node + "'s TPM"
	if len(p.Secrets) > 0 && p.Secrets[0].SealedTo == "host" {
		sealed = "a key only root on " + p.Placement.Node + " can read"
	}
	if secrets == nil {
		row("Secrets", "None")
	} else {
		row("Secrets", fmt.Sprintf("%d, made on %s and sealed to %s: %s", len(secrets), p.Placement.Node, sealed, strings.Join(secrets, ", ")))
	}

	r := p.Resources
	disk := fmt.Sprintf("Disk: needs %s, %s free", DiskSize(r.Disk.NeedsBytes), DiskSize(r.Disk.FreeBytes))
	if r.Disk.Unknown {
		disk = fmt.Sprintf("Disk: needs %s; %s can't say how much is free", DiskSize(r.Disk.NeedsBytes), p.Placement.Node)
	}
	if r.Disk.Grows != "" {
		disk += ", then grows with " + r.Disk.Grows
	}
	gpu := "GPU: not needed"
	switch {
	case r.GPU.Need != "none" && len(r.GPU.Usable) > 0:
		gpu = fmt.Sprintf("GPU: %s, for %s", strings.Join(r.GPU.Usable, ", "), r.GPU.For)
	case r.GPU.Need == "optional":
		gpu = "GPU: none it can use; it runs without one, " + r.GPU.For + " is slower"
	case r.GPU.Need == "required":
		gpu = "GPU: none it can use"
	}
	row("Resources", fmt.Sprintf("Memory: needs %s, %s available. %s. %s.", gb(r.Memory.NeedsBytes), gb(r.Memory.FreeBytes), disk, gpu))
	for _, x := range r.Warnings {
		switch x.Kind {
		case "memory":
			row("Warning", fmt.Sprintf("Not enough memory: %s needs %s, %s has %s available.", p.App.Name, gb(x.NeedsBytes), p.Placement.Node, gb(x.FreeBytes)))
		case "disk":
			if x.Unknown {
				row("Warning", fmt.Sprintf("%s can't say how much disk is free; %s needs %s.", p.Placement.Node, p.App.Name, DiskSize(x.NeedsBytes)))
				continue
			}
			row("Warning", fmt.Sprintf("Not enough disk: %s needs %s, %s has %s free.", p.App.Name, DiskSize(x.NeedsBytes), p.Placement.Node, DiskSize(x.FreeBytes)))
		case "gpu":
			row("Warning", fmt.Sprintf("%s needs a GPU from %s, and %s has none.", p.App.Name, strings.Join(r.GPU.Vendors, " or "), p.Placement.Node))
		case "build":
			row("Warning", fmt.Sprintf("No build for %s on %s: %s.", p.Placement.Architecture, upperFirst(string(p.Placement.Engine)), strings.Join(x.Services, ", ")))
		}
	}
	fmt.Fprint(w, "\nNothing happens until the plan is applied; applying comes with a later version of HomersLab.\n")
}

// gb is memory in whole gigabytes (GiB), rounded, or in megabytes (MiB) below one.
func gb(b uint64) string {
	if b < 1<<30 {
		return fmt.Sprintf("%d MB", b>>20)
	}
	return fmt.Sprintf("%.0f GB", math.Round(float64(b)/(1<<30)))
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// lowerFirst lowercases a phrase's first letter, unless its first word is a name ("BentoPDF", "Immich's").
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	first, _, _ := strings.Cut(s, " ")
	if strings.IndexFunc(first[n:], unicode.IsUpper) >= 0 {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}
