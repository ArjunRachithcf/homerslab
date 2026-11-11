package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/host"
)

// firewallRestore is `homerslab firewall restore`, run by homerslab-firewall-restore.service before the network,
// the engines and the mesh client start: it loads the table the agent last applied, or a closed one on the first
// boot. The engines require it, so they never start without a table.
func firewallRestore(ctx context.Context, args []string, env Env) error {
	fs := flags("firewall restore", env)
	state := fs.String("state-dir", DefaultAgentState, "the agent's state, where it keeps the table it last applied")
	setupPort := fs.Int("setup-port", DefaultSetupPort, "the setup listener's port, open in the first boot's closed table")
	print := fs.Bool("print", false, "print the table instead of loading it")
	if err := parse(fs, args); err != nil {
		return err
	}
	script, err := firewall.RestoreScript(filepath.Join(*state, "firewall.nft"), *setupPort)
	if err != nil {
		return err
	}
	if *print {
		_, err := fmt.Fprint(env.Stdout, script)
		return err
	}
	if env.Euid() != 0 {
		return fmt.Errorf("loading the firewall table needs root")
	}
	_, err = host.Exec(ctx, host.Cmd{Name: "nft", Args: []string{"-f", "-"}, Stdin: []byte(script)})
	return err
}
