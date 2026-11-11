package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/host"
)

// firewallRestore is `homerslab firewall restore`, run by homerslab-firewall-restore.service before the network,
// the engines and the mesh client start: it loads the table the agent last applied, or a closed one on the first
// boot or when the saved table can't be used (it says so, and succeeds: the closed table is in place). The closed
// table's LAN exception is the manager's setup port, as the agent last heard it. The engines require it, so they
// never start without a table.
func firewallRestore(ctx context.Context, args []string, env Env) error {
	fs := flags("firewall restore", env)
	state := fs.String("state-dir", DefaultAgentState, "the agent's state, where it keeps the table it last applied")
	setupPort := fs.Int("setup-port", DefaultSetupPort, "the setup listener's port, open in the first boot's closed table, "+
		"until the manager has given the agent its own")
	print := fs.Bool("print", false, "print the table instead of loading it")
	if err := parse(fs, args); err != nil {
		return err
	}
	port := *setupPort
	if p, ok := agent.SavedSetupPort(*state); ok {
		port = p
	}
	script, unusable := firewall.RestoreScript(filepath.Join(*state, "firewall.nft"), port)
	if unusable != nil {
		fmt.Fprintf(env.Stderr, "homerslab: the saved firewall table can't be used, so the closed one is loaded: %v\n", unusable)
	}
	if *print {
		_, err := fmt.Fprint(env.Stdout, script)
		return err
	}
	if env.Euid() != 0 {
		return fmt.Errorf("loading the firewall table needs root")
	}
	_, err := host.Exec(ctx, host.Cmd{Name: "nft", Args: []string{"-f", "-"}, Stdin: []byte(script)})
	return err
}
