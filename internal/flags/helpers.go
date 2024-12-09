// Copyright 2020 The go-ethereum Authors
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
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/internal/version"
	"github.com/ethereum/go-ethereum/log"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"
)

// usecolor defines whether the CLI help should use colored output or normal dumb
// colorless terminal formatting.
var usecolor = (isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())) && os.Getenv("TERM") != "dumb"

// NewApp creates an app with sane defaults.
func NewApp(usage string) *cli.Command {
	git, _ := version.VCS()
	return &cli.Command{
		CustomRootCommandHelpTemplate: rootCommandTemplate(),
		EnableShellCompletion:         true,
		Version:                       version.WithCommit(git.Commit, git.Date),
		Usage:                         usage,
		Copyright:                     "Copyright 2013-2025 The go-ethereum Authors",
	}
}

func rootCommandTemplate() string {
	tpl := regexp.MustCompile("[A-Z ]+:").ReplaceAllString(cli.RootCommandHelpTemplate, "\u001B[33m$0\u001B[0m")
	return strings.ReplaceAll(tpl, "{{template \"visibleFlagCategoryTemplate\" .}}", "{{range .VisibleFlagCategories}}\n   {{if .Name}}\u001B[33m{{.Name}}\u001B[0m\n\n   {{end}}{{$flglen := len .Flags}}{{range $i, $e := .Flags}}{{if eq (subtract $flglen $i) 1}}{{$e}}\n{{else}}{{$e}}\n   {{end}}{{end}}{{end}}")
}

func init() {
	cli.FlagStringer = FlagString
}

// FlagString prints a single flag in help.
func FlagString(f cli.Flag) string {
	df, ok := f.(cli.DocGenerationFlag)
	if !ok {
		return ""
	}
	needsPlaceholder := df.TakesValue()
	placeholder := ""
	if needsPlaceholder {
		placeholder = "value"
	}

	namesText := cli.FlagNamePrefixer(f.Names(), placeholder)

	defaultValueString := ""
	if s := df.GetDefaultText(); s != "" {
		defaultValueString = " (default: " + s + ")"
	}
	envHint := strings.TrimSpace(cli.FlagEnvHinter(df.GetEnvVars(), ""))
	if envHint != "" {
		envHint = " (" + envHint[1:len(envHint)-1] + ")"
	}
	usage := strings.TrimSpace(df.GetUsage())
	usage = wordWrap(usage, 80)
	usage = indent(usage, 10)

	if usecolor {
		return fmt.Sprintf("\n    \u001B[32m%-35s%-35s\u001B[0m%s\n%s", namesText, defaultValueString, envHint, usage)
	} else {
		return fmt.Sprintf("\n    %-35s%-35s%s\n%s", namesText, defaultValueString, envHint, usage)
	}
}

func indent(s string, nspace int) string {
	ind := strings.Repeat(" ", nspace)
	return ind + strings.ReplaceAll(s, "\n", "\n"+ind)
}

func wordWrap(s string, width int) string {
	var (
		output     strings.Builder
		lineLength = 0
	)

	for {
		sp := strings.IndexByte(s, ' ')
		var word string
		if sp == -1 {
			word = s
		} else {
			word = s[:sp]
		}
		wlen := len(word)
		over := lineLength+wlen >= width
		if over {
			output.WriteByte('\n')
			lineLength = 0
		} else {
			if lineLength != 0 {
				output.WriteByte(' ')
				lineLength++
			}
		}

		output.WriteString(word)
		lineLength += wlen

		if sp == -1 {
			break
		}
		s = s[wlen+1:]
	}

	return output.String()
}

// AutoEnvVars extends all the specific CLI flags with automatically generated
// env vars by capitalizing the flag, replacing . with _ and prefixing it with
// the specified string.
//
// Note, the prefix should *not* contain the separator underscore, that will be
// added automatically.
func AutoEnvVars(flags []cli.Flag, prefix string) {
	for _, flag := range flags {
		envvar := strings.ToUpper(prefix + "_" + strings.ReplaceAll(strings.ReplaceAll(flag.Names()[0], ".", "_"), "-", "_"))
		source := cli.EnvVars(envvar)

		switch flag := flag.(type) {
		case *cli.StringFlag:
			flag.Sources.Append(source)
		case *cli.StringSliceFlag:
			flag.Sources.Append(source)
		case *cli.BoolFlag:
			flag.Sources.Append(source)
		case *cli.IntFlag:
			flag.Sources.Append(source)
		case *cli.UintFlag:
			flag.Sources.Append(source)
		case *cli.FloatFlag:
			flag.Sources.Append(source)
		case *cli.DurationFlag:
			flag.Sources.Append(source)
		case *BigFlag:
			flag.Sources.Append(source)
		case *DirectoryFlag:
			flag.Sources.Append(source)
		}
	}
}

// CheckEnvVars iterates over all the environment variables and checks if any of
// them look like a CLI flag but is not consumed. This can be used to detect old
// or mistyped names.
func CheckEnvVars(ctx *cli.Command, flags []cli.Flag, prefix string) {
	known := make(map[string]string)
	for _, flag := range flags {
		docflag, ok := flag.(cli.DocGenerationFlag)
		if !ok {
			continue
		}
		for _, envvar := range docflag.GetEnvVars() {
			known[envvar] = flag.Names()[0]
		}
	}
	keyvals := os.Environ()
	sort.Strings(keyvals)

	for _, keyval := range keyvals {
		key := strings.Split(keyval, "=")[0]
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if flag, ok := known[key]; ok {
			if ctx.Count(flag) > 0 {
				log.Info("Config environment variable found", "envvar", key, "shadowedby", "--"+flag)
			} else {
				log.Info("Config environment variable found", "envvar", key)
			}
		} else {
			log.Warn("Unknown config environment variable", "envvar", key)
		}
	}
}
