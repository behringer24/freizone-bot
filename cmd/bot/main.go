// Command freizone-bot is an automation daemon for Freizone: it holds a
// Freizone account and connects Freizone to other systems in both directions.
// See ../../README.md.
//
// One binary, several subcommands, in the shape freizone-server's own devclient
// uses: os.Args[1] selects, and each subcommand owns its own flag set. The
// daemon and the tools that talk to it have to be the same binary anyway --
// the container has no shell, so `docker exec <container> /freizone-bot ...`
// is the only way in.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runDaemon(os.Args[2:])
	case "send":
		err = runSend(os.Args[2:])
	case "status":
		err = runStatus(os.Args[2:])
	case "whoami":
		err = runWhoami(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "freizone-bot: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "freizone-bot: "+readable(err))
		// A specific exit code where there is one to give: this ends up inside
		// shell scripts, and a script can only branch on a number.
		var coded *exitError
		if errors.As(err, &coded) {
			os.Exit(coded.code)
		}
		os.Exit(1)
	}
}

// readable strips the `client:` package prefix every error pkg/client raises
// carries. It classifies nothing for whoever is reading the terminal -- it is a
// Go package name in front of a sentence written for them -- and this binary
// prefixes its own name anyway, so leaving it in means two prefixes before the
// first word that matters. freizone-app strips the same one for the same
// reason.
func readable(err error) string {
	const prefix = "client: "
	msg := err.Error()
	return strings.TrimPrefix(msg, prefix)
}

func usage() {
	fmt.Fprint(os.Stderr, `freizone-bot -- an automation daemon for Freizone

Usage:
  freizone-bot run                    run the daemon
  freizone-bot send [flags] [TEXT]    send a message through the running daemon
  freizone-bot status                 ask the running daemon how it is doing
  freizone-bot whoami                 print this bot's own Freizone address

send takes its text as an argument, or on standard input when no argument is
given -- never both, so that a stdin nobody closes cannot make it hang. Run a
subcommand with -h for its flags.

Configuration is by environment variable; see the configuration reference in
README.md. FREIZONE_BOT_SERVER is the only one without a default.

send exits 0 when the message is durably queued, 1 on a usage or configuration
problem, 2 when the daemon refused it, 3 on a daemon error, 4 when no daemon is
running, and 5 on a timeout.
`)
}
