package wscli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// resetFlagState zeros every flag-backing global so that a previous Run's
// --status, --workspace, etc. cannot bleed into the next one. It walks the
// cobra tree and writes each flag's DefValue back into its Value, which
// covers both PersistentFlags on rootCmd and per-subcommand flags.
//
// initConfig is registered via cobra.OnInitialize and re-runs on every
// Execute, so config flows through the precedence chain afresh each call.
func resetFlagState() {
	resetCommandFlags(rootCmd)
	cfg = Config{StatusAliases: map[string]string{}}
}

func resetCommandFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(resetFlag)
	cmd.PersistentFlags().VisitAll(resetFlag)
	for _, child := range cmd.Commands() {
		resetCommandFlags(child)
	}
}

// resetFlag restores a single flag to its declared default, but only if the
// user actually set it on the previous Run. Skipping pristine flags matters
// for slice-valued flags whose DefValue stringifies to "[]": pflag's
// StringSlice.Set parses "[]" as a one-element CSV (the literal string
// "[]"), so blindly re-applying DefValue would poison an untouched
// --label filter with a bogus entry. Only user-mutated flags need
// restoring; the rest are already at their declared default.
func resetFlag(f *pflag.Flag) {
	if !f.Changed {
		return
	}
	_ = f.Value.Set(f.DefValue)
	f.Changed = false
}
