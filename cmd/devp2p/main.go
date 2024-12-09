// Copyright 2019 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/internal/debug"
	"github.com/ethereum/go-ethereum/internal/flags"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/urfave/cli/v3"
)

var app = flags.NewApp("go-ethereum devp2p tool")

func init() {
	app.Flags = append(app.Flags, debug.Flags...)
	app.Before = func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		// err := error(nil)
		err := debug.Setup(cmd)
		return ctx, err
	}
	app.After = func(ctx context.Context, cmd *cli.Command) error {
		debug.Exit()
		return nil
	}

	// Add subcommands.
	app.Commands = []*cli.Command{
		enrdumpCommand,
		keyCommand,
		discv4Command,
		discv5Command,
		dnsCommand,
		nodesetCommand,
		rlpxCommand,
	}
}

func main() {
	exit(app.Run(context.Background(), os.Args))
}

// commandHasFlag returns true if the current command supports the given flag.
func commandHasFlag(cmd *cli.Command, flag cli.Flag) bool {
	names := flag.Names()
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	for _, cmd := range cmd.Lineage() {
		for _, f := range cmd.Flags {
			for _, name := range f.Names() {
				if _, ok := set[name]; ok {
					return true
				}
			}
		}
	}
	return false
}

// getNodeArg handles the common case of a single node descriptor argument.
func getNodeArg(cmd *cli.Command) *enode.Node {
	if cmd.NArg() < 1 {
		exit("missing node as command-line argument")
	}
	n, err := parseNode(cmd.Args().First())
	if err != nil {
		exit(err)
	}
	return n
}

func exit(err any) {
	if err == nil {
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
