// Copyright 2016 The go-ethereum Authors
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

package debug

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	_ "net/http/pprof"

	"github.com/ethereum/go-ethereum/internal/flags"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/metrics/exp"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	verbosityFlag = &cli.IntFlag{
		Name:     "verbosity",
		Usage:    "Logging verbosity: 0=silent, 1=error, 2=warn, 3=info, 4=debug, 5=detail",
		Value:    3,
		Category: flags.LoggingCategory,
	}
	logVmoduleFlag = &cli.StringFlag{
		Name:     "log.vmodule",
		Usage:    "Per-module verbosity: comma-separated list of <pattern>=<level> (e.g. eth/*=5,p2p=4)",
		Category: flags.LoggingCategory,
	}
	logFormatFlag = &cli.StringFlag{
		Name:     "log.format",
		Usage:    "Log format to use (json|logfmt|terminal)",
		Value:    "terminal",
		Category: flags.LoggingCategory,
	}
	logFileFlag = &cli.StringFlag{
		Name:     "log.file",
		Usage:    "Write logs to a file",
		Category: flags.LoggingCategory,
	}
	logRotateFlag = &cli.BoolFlag{
		Name:     "log.rotate",
		Usage:    "Enables log file rotation",
		Category: flags.LoggingCategory,
	}
	logMaxSizeMBsFlag = &cli.UintFlag{
		Name:     "log.maxsize",
		Usage:    "Maximum size in MBs of a single log file",
		Value:    100,
		Category: flags.LoggingCategory,
	}
	logMaxBackupsFlag = &cli.UintFlag{
		Name:     "log.maxbackups",
		Usage:    "Maximum number of log files to retain",
		Value:    10,
		Category: flags.LoggingCategory,
	}
	logMaxAgeFlag = &cli.UintFlag{
		Name:     "log.maxage",
		Usage:    "Maximum number of days to retain a log file",
		Value:    30,
		Category: flags.LoggingCategory,
	}
	logCompressFlag = &cli.BoolFlag{
		Name:     "log.compress",
		Usage:    "Compress the log files",
		Category: flags.LoggingCategory,
	}
	pprofFlag = &cli.BoolFlag{
		Name:     "pprof",
		Usage:    "Enable the pprof HTTP server",
		Category: flags.LoggingCategory,
	}
	pprofPortFlag = &cli.UintFlag{
		Name:     "pprof.port",
		Usage:    "pprof HTTP server listening port",
		Value:    6060,
		Category: flags.LoggingCategory,
	}
	pprofAddrFlag = &cli.StringFlag{
		Name:     "pprof.addr",
		Usage:    "pprof HTTP server listening interface",
		Value:    "127.0.0.1",
		Category: flags.LoggingCategory,
	}
	memprofilerateFlag = &cli.IntFlag{
		Name:  "pprof.memprofilerate",
		Usage: "Turn on memory profiling with the given rate",
		Action: func(ctx context.Context, cmd *cli.Command, value int64) error {
			runtime.MemProfileRate = int(value)
			return nil
		},
		Category: flags.LoggingCategory,
	}
	blockprofilerateFlag = &cli.IntFlag{
		Name:     "pprof.blockprofilerate",
		Usage:    "Turn on block profiling with the given rate",
		Category: flags.LoggingCategory,
	}
	cpuprofileFlag = &cli.StringFlag{
		Name:     "pprof.cpuprofile",
		Usage:    "Write CPU profile to the given file",
		Category: flags.LoggingCategory,
	}
	goTraceFlag = &cli.StringFlag{
		Name:     "go-execution-trace",
		Usage:    "Write Go execution trace to the given file",
		Category: flags.LoggingCategory,
	}
)

// Deprecated flags.
var (
	vmoduleFlag = &cli.StringFlag{
		Name:     "vmodule",
		Usage:    "Per-module verbosity: comma-separated list of <pattern>=<level> (e.g. eth/*=5,p2p=4)",
		Hidden:   true, // deprecated, don't show in help
		Category: flags.DeprecatedCategory,
	}
	logjsonFlag = &cli.BoolFlag{
		Name:     "log.json",
		Usage:    "Format logs with JSON",
		Hidden:   true, // deprecated, don't show in help
		Category: flags.DeprecatedCategory,
	}
)

// Flags holds all command-line flags required for debugging.
var Flags = []cli.Flag{
	verbosityFlag,
	logVmoduleFlag,
	vmoduleFlag,
	logjsonFlag,
	logFormatFlag,
	logFileFlag,
	logRotateFlag,
	logMaxSizeMBsFlag,
	logMaxBackupsFlag,
	logMaxAgeFlag,
	logCompressFlag,
	pprofFlag,
	pprofAddrFlag,
	pprofPortFlag,
	memprofilerateFlag,
	blockprofilerateFlag,
	cpuprofileFlag,
	goTraceFlag,
}

var (
	glogger       *log.GlogHandler
	logOutputFile io.WriteCloser
)

func init() {
	glogger = log.NewGlogHandler(log.NewTerminalHandler(os.Stderr, false))
}

// Setup initializes profiling and logging based on the CLI flags.
// It should be called as early as possible in the program.
func Setup(cmd *cli.Command) error {
	var (
		handler        slog.Handler
		terminalOutput = io.Writer(os.Stderr)
		output         io.Writer

		logFile          = cmd.String(logFileFlag.Name)
		logFormat        = cmd.String(logFormatFlag.Name)
		logRotateEnabled = cmd.Bool(logRotateFlag.Name)
	)
	if len(logFile) > 0 {
		if err := validateLogLocation(filepath.Dir(logFile)); err != nil {
			return fmt.Errorf("failed to initiatilize file logger: %v", err)
		}
	}
	context := []any{"format", logFormat, "rotate", logRotateEnabled}

	if logRotateEnabled {
		// Lumberjack uses <processname>-lumberjack.log in is.TempDir() if empty.
		// so typically /tmp/geth-lumberjack.log on linux
		if len(logFile) > 0 {
			context = append(context, "location", logFile)
		} else {
			context = append(context, "location", filepath.Join(os.TempDir(), "geth-lumberjack.log"))
		}
		logOutputFile = &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    int(cmd.Int(logMaxSizeMBsFlag.Name)),
			MaxBackups: int(cmd.Int(logMaxBackupsFlag.Name)),
			MaxAge:     int(cmd.Int(logMaxAgeFlag.Name)),
			Compress:   cmd.Bool(logCompressFlag.Name),
		}
		output = io.MultiWriter(terminalOutput, logOutputFile)
	} else if logFile != "" {
		var err error
		if logOutputFile, err = os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err != nil {
			return err
		}
		output = io.MultiWriter(logOutputFile, terminalOutput)
		context = append(context, "location", logFile)
	} else {
		output = terminalOutput
	}

	var deprecatedLogJSONUsed bool
	if cmd.IsSet(logjsonFlag.Name) {
		deprecatedLogJSONUsed = true
		if cmd.Bool(logjsonFlag.Name) {
			logFormat = "json"
		}
	}

	switch {
	case logFormat == "json":
		handler = log.JSONHandler(output)
	case logFormat == "logfmt":
		handler = log.LogfmtHandler(output)
	case logFormat == "", logFormat == "terminal":
		useColor := (isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())) && os.Getenv("TERM") != "dumb"
		if useColor {
			terminalOutput = colorable.NewColorableStderr()
			if logOutputFile != nil {
				output = io.MultiWriter(logOutputFile, terminalOutput)
			} else {
				output = terminalOutput
			}
		}
		handler = log.NewTerminalHandler(output, useColor)
	default:
		// Unknown log format specified
		return fmt.Errorf("unknown log format: %v", logFormat)
	}
	glogger = log.NewGlogHandler(handler)

	// logging
	verbosity := log.FromLegacyLevel(int(cmd.Int(verbosityFlag.Name)))
	vmodule := cmd.String(vmoduleFlag.Name)
	if cmd.IsSet(logVmoduleFlag.Name) {
		vmodule = cmd.String(logVmoduleFlag.Name)
	}
	glogger.Verbosity(verbosity)
	glogger.Vmodule(vmodule)
	log.SetDefault(log.NewLogger(glogger))

	// Print deprecation notices. This needs to be done after logging is initialized.
	if deprecatedLogJSONUsed {
		log.Warn("Command-line flag --log.json is deprecated. Please use --log.format=json instead.")
	}
	if cmd.IsSet(vmoduleFlag.Name) {
		log.Warn("Command-line flag --vmodule is deprecated. Please use --log.vmodule instead.")
	}

	// profiling, tracing
	if cmd.IsSet(blockprofilerateFlag.Name) {
		Handler.SetBlockProfileRate(int(cmd.Int(blockprofilerateFlag.Name)))
	}
	if cmd.IsSet(goTraceFlag.Name) {
		if err := Handler.StartGoTrace(cmd.String(goTraceFlag.Name)); err != nil {
			return err
		}
	}
	if cmd.IsSet(cpuprofileFlag.Name) {
		if err := Handler.StartCPUProfile(cmd.String(cpuprofileFlag.Name)); err != nil {
			return err
		}
	}

	// pprof server
	if cmd.Bool(pprofFlag.Name) {
		addr := cmd.String(pprofAddrFlag.Name)
		port := cmd.Int(pprofPortFlag.Name)
		address := net.JoinHostPort(addr, fmt.Sprintf("%d", port))
		// This context value ("metrics.addr") represents the utils.MetricsHTTPFlag.Name.
		// It cannot be imported because it will cause a cyclical dependency.
		//
		// TODO(fjl): move this to package metrics setup
		StartPProf(address, !cmd.IsSet("metrics.addr"))
	}
	if len(logFile) > 0 || logRotateEnabled {
		log.Info("Logging configured", context...)
	}
	return nil
}

func StartPProf(address string, withMetrics bool) {
	// Hook go-metrics into expvar on any /debug/metrics request, load all vars
	// from the registry into expvar, and execute regular expvar handler.
	if withMetrics {
		exp.Exp(metrics.DefaultRegistry)
	}
	log.Info("Starting pprof server", "addr", fmt.Sprintf("http://%s/debug/pprof", address))
	go func() {
		if err := http.ListenAndServe(address, nil); err != nil {
			log.Error("Failure in running pprof server", "err", err)
		}
	}()
}

// Exit stops all running profiles, flushing their output to the
// respective file.
func Exit() {
	Handler.StopCPUProfile()
	Handler.StopGoTrace()
	if logOutputFile != nil {
		logOutputFile.Close()
	}
}

func validateLogLocation(path string) error {
	if err := os.MkdirAll(path, os.ModePerm); err != nil {
		return fmt.Errorf("error creating the directory: %w", err)
	}
	// Check if the path is writable by trying to create a temporary file
	tmp := filepath.Join(path, "tmp")
	if f, err := os.Create(tmp); err != nil {
		return err
	} else {
		f.Close()
	}
	return os.Remove(tmp)
}
