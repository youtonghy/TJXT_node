package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var command = &cobra.Command{
	Use: "tjxt-node",
	// Errors are printed once by Run; usage only helps with flag mistakes,
	// which cobra still reports.
	SilenceErrors: true,
	SilenceUsage:  true,
}

// exitError is a command failure with a specific process exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// Run executes the command line and exits non-zero when the command fails.
func Run() {
	err := command.Execute()
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "tjxt-node: %v\n", err)
	code := 1
	var ee *exitError
	if errors.As(err, &ee) {
		code = ee.code
	}
	os.Exit(code)
}
