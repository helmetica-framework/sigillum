package cmd

import (
	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
)

var RootCmd = &cobra.Command{
	Use:   "sigillum",
	Short: "sigillum manages network configs.",
	Long:  "sigillum manages network configs.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cmd.SilenceUsage = true
	},
}

func Execute() {
	lifetimeCtx := ctrl.SetupSignalHandler()

	RootCmd.ExecuteContext(lifetimeCtx)
}
