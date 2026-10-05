package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Construct-Linux/os/internal/onepassword"
)

// onePasswordCmd is construct 1password install | update (internal/onepassword): install from the
// launcher the first time it is opened, update from the weekly timer where it is installed.
func onePasswordCmd(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 1 || (args[0] != "install" && args[0] != "update") {
		return errors.New("1password: install, or update (what the weekly timer runs)")
	}
	if args[0] == "update" && !onepassword.Installed(onepassword.Dir) {
		return nil
	}
	version, err := onepassword.Install(ctx, onepassword.Options{URL: onepassword.URL, Key: onepassword.Key, Dir: onepassword.Dir})
	switch {
	case err != nil:
		return err
	case version == "":
		fmt.Fprintln(stdout, "1Password is up to date")
	default:
		fmt.Fprintf(stdout, "installed 1Password %s\n", version)
	}
	return nil
}
