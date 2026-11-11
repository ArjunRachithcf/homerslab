package cli

import (
	"context"
	"errors"
	"fmt"
)

// meshOwnerPassword is `sudo homerslab mesh owner-password`: the owner's password of a NetBird HomersLab set up
// itself, for its dashboard when every token is lost. Root, at this machine, over the control socket only.
func meshOwnerPassword(ctx context.Context, args []string, env Env) error {
	fs := flags("mesh owner-password", env)
	socket := fs.String("control-socket", DefaultControlSocket, "the manager's control socket")
	if err := parse(fs, args); err != nil {
		return err
	}
	if env.Euid() != 0 {
		return errors.New("the mesh owner's password is for root at this machine: run it with sudo")
	}
	var v struct {
		Password string `json:"password"`
	}
	_, err := newControl(*socket).do(ctx, "GET", "/api/v1/mesh/owner-password", 0, &v)
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == "no-owner-password" {
		return errors.New("HomersLab didn't set up this mesh, so it has no owner password: sign in at your mesh's dashboard")
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(env.Stderr, "This is the password of your NetBird's owner: it opens NetBird's dashboard, and everything in it. "+
		"Keep it in your password manager, and clear this terminal.")
	fmt.Fprintln(env.Stdout, v.Password)
	return nil
}
