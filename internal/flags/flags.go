// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package flags

import (
	"errors"
	"math/big"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/common/math"
	"github.com/urfave/cli/v3"
)

// DirectoryFlag is custom cli.Flag type which expand the received string to an absolute path.
// e.g. ~/.ethereum -> /home/username/.ethereum
type DirectoryFlag = cli.FlagBase[string, cli.NoConfig, DirectoryString]

// DirectoryString is custom type which is registered in the flags library which cli uses for
// argument parsing. This allows us to expand Value to an absolute path when
// the argument is parsed
type DirectoryString string

func (s DirectoryString) Create(val string, p *string, c cli.NoConfig) cli.Value {
	*p = val
	wrapper := DirectoryString(val)
	return &wrapper
}

func (s DirectoryString) ToString(val string) string {
	return val
}

func (s *DirectoryString) String() string {
	return string(*s)
}

func (s *DirectoryString) Get() any {
	return string(*s)
}

func (s *DirectoryString) Set(value string) error {
	*s = DirectoryString(expandPath(value))
	return nil
}

// BigFlag is a command line flag that accepts 256 bit big integers in decimal or
// hexadecimal syntax.
type BigFlag = cli.FlagBase[*big.Int, cli.NoConfig, bigValue]

// bigValue implements cli.Value and cli.ValueCreator
type bigValue struct {
	val *big.Int
}

func (b bigValue) Create(val *big.Int, p **big.Int, c cli.NoConfig) cli.Value {
	*p = val
	return &bigValue{val: val}
}

func (b bigValue) ToString(v *big.Int) string {
	return v.String()
}

func (b *bigValue) String() string {
	if b == nil {
		return ""
	}
	return b.val.String()
}

func (b *bigValue) Get() any {
	return b.val
}

func (b *bigValue) Set(s string) error {
	intVal, ok := math.ParseBig256(s)
	if !ok {
		return errors.New("invalid integer syntax")
	}
	b.val = intVal
	return nil
}

// GlobalBig returns the value of a BigFlag from the global flag set.
func GlobalBig(ctx *cli.Command, name string) *big.Int {
	val := ctx.Generic(name)
	if val == nil {
		return nil
	}
	return val.Get().(*big.Int)
}

// Expands a file path
// 1. replace tilde with users home dir
// 2. expands embedded environment variables
// 3. cleans the path, e.g. /a/b/../c -> /a/c
// Note, it has limitations, e.g. ~someuser/tmp will not be expanded
func expandPath(p string) string {
	// Named pipes are not file paths on windows, ignore
	if strings.HasPrefix(p, `\\.\pipe`) {
		return p
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		if home := HomeDir(); home != "" {
			p = home + p[1:]
		}
	}
	return filepath.Clean(os.ExpandEnv(p))
}

func HomeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if usr, err := user.Current(); err == nil {
		return usr.HomeDir
	}
	return ""
}

func eachName(f cli.Flag, fn func(string)) {
	for _, name := range f.Names() {
		name = strings.Trim(name, " ")
		fn(name)
	}
}
