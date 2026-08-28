package cmd

import (
	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
)

var RootCmd = &cobra.Command{
	Use:   "sigillum",
	Short: "sigillum manages seals.",
	Long:  "sigillum manages seals.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cmd.SilenceUsage = true
	},
}

func Execute() {
	lifetimeCtx := ctrl.SetupSignalHandler()

	RootCmd.ExecuteContext(lifetimeCtx)
}
