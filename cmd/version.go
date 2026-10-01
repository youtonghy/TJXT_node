package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	version  = "dev" // replaced by -ldflags at release build
	codename = "tjxt-node"
	intro    = "TONG-ladder node agent based on Xray-core"
)

var versionCommand = cobra.Command{
	Use:   "version",
	Short: "Print version info",
	Run: func(_ *cobra.Command, _ []string) {
		showVersion()
	},
}

func init() {
	command.AddCommand(&versionCommand)
}

func showVersion() {
	fmt.Printf("%s %s (%s) \n", codename, version, intro)
}
