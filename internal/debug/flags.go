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
	globalVerbosity        = int64(3)
	globalVmodule          string
	globalLogFormat        = "terminal"
	globalLogJSONUsed      bool
	globalLogFile          string
	globalLogRotateEnabled bool
	globalLogMaxSize       = uint64(100)
	globalLogMaxAge        = uint64(30)
	globalLogMaxBackups    = uint64(10)
	globalLogCompress      = false
	globalPprofEnabled     = false
	globalPprofAddr        = "127.0.0.1"
	globalPprofPort        = uint64(6060)
	globalBlockprofileRate = int64(-1)
	globalCPUProfileFile   string
	globalGoTraceFile      string
)

var (
	globalLogJSON bool
)

var (
	verbosityFlag = &cli.IntFlag{
		Name:        "verbosity",
		Usage:       "Logging verbosity: 0=silent, 1=error, 2=warn, 3=info, 4=debug, 5=detail",
		Destination: &globalVerbosity,
		Value:       globalVerbosity,
		Category:    flags.LoggingCategory,
	}
	logVmoduleFlag = &cli.StringFlag{
		Name:        "log.vmodule",
		Usage:       "Per-module verbosity: comma-separated list of <pattern>=<level> (e.g. eth/*=5,p2p=4)",
		Destination: &globalVmodule,
		Value:       globalVmodule,
		Category:    flags.LoggingCategory,
	}
	logFormatFlag = &cli.StringFlag{
		Name:        "log.format",
		Usage:       "Log format to use (json|logfmt|terminal)",
		Destination: &globalLogFormat,
		Value:       globalLogFormat,
		Category:    flags.LoggingCategory,
	}
	logFileFlag = &cli.StringFlag{
		Name:        "log.file",
		Usage:       "Write logs to a file",
		Destination: &globalLogFile,
		Category:    flags.LoggingCategory,
	}
	logRotateFlag = &cli.BoolFlag{
		Name:        "log.rotate",
		Usage:       "Enables log file rotation",
		Destination: &globalLogRotateEnabled,
		Category:    flags.LoggingCategory,
	}
	logMaxSizeMBsFlag = &cli.UintFlag{
		Name:        "log.maxsize",
		Usage:       "Maximum size in MBs of a single log file",
		Destination: &globalLogMaxSize,
		Value:       globalLogMaxSize,
		Category:    flags.LoggingCategory,
	}
	logMaxBackupsFlag = &cli.UintFlag{
		Name:        "log.maxbackups",
		Usage:       "Maximum number of log files to retain",
		Destination: &globalLogMaxBackups,
		Value:       globalLogMaxBackups,
		Category:    flags.LoggingCategory,
	}
	logMaxAgeFlag = &cli.UintFlag{
		Name:        "log.maxage",
		Usage:       "Maximum number of days to retain a log file",
		Destination: &globalLogMaxAge,
		Value:       globalLogMaxAge,
		Category:    flags.LoggingCategory,
	}
	logCompressFlag = &cli.BoolFlag{
		Name:        "log.compress",
		Usage:       "Compress the log files",
		Destination: &globalLogCompress,
		Value:       globalLogCompress,
		Category:    flags.LoggingCategory,
	}
	pprofFlag = &cli.BoolFlag{
		Name:        "pprof",
		Usage:       "Enable the pprof HTTP server",
		Destination: &globalPprofEnabled,
		Category:    flags.LoggingCategory,
	}
	pprofPortFlag = &cli.UintFlag{
		Name:        "pprof.port",
		Usage:       "pprof HTTP server listening port",
		Destination: &globalPprofPort,
		Value:       globalPprofPort,
		Category:    flags.LoggingCategory,
	}
	pprofAddrFlag = &cli.StringFlag{
		Name:        "pprof.addr",
		Usage:       "pprof HTTP server listening interface",
		Destination: &globalPprofAddr,
		Value:       globalPprofAddr,
		Category:    flags.LoggingCategory,
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
		Name:        "pprof.blockprofilerate",
		Usage:       "Turn on block profiling with the given rate",
		Destination: &globalBlockprofileRate,
		Category:    flags.LoggingCategory,
	}
	cpuprofileFlag = &cli.StringFlag{
		Name:        "pprof.cpuprofile",
		Usage:       "Write CPU profile to the given file",
		Destination: &globalCPUProfileFile,
		Category:    flags.LoggingCategory,
	}
	traceFlag = &cli.StringFlag{
		Name:        "go-execution-trace",
		Usage:       "Write Go execution trace to the given file",
		Destination: &globalGoTraceFile,
		Category:    flags.LoggingCategory,
	}
)

// Deprecated flags.
var (
	vmoduleFlag = &cli.StringFlag{
		Name:        "vmodule",
		Usage:       "Per-module verbosity: comma-separated list of <pattern>=<level> (e.g. eth/*=5,p2p=4)",
		Destination: &globalVmodule,
		Hidden:      true, // deprecated, don't show in help
		Category:    flags.DeprecatedCategory,
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
	traceFlag,
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
	)
	if len(globalLogFile) > 0 {
		if err := validateLogLocation(filepath.Dir(globalLogFile)); err != nil {
			return fmt.Errorf("failed to initiatilize file logger: %v", err)
		}
	}
	context := []any{"format", globalLogFormat, "rotate", globalLogRotateEnabled}

	if globalLogRotateEnabled {
		// Lumberjack uses <processname>-lumberjack.log in is.TempDir() if empty.
		// so typically /tmp/geth-lumberjack.log on linux
		if len(globalLogFile) > 0 {
			context = append(context, "location", globalLogFile)
		} else {
			context = append(context, "location", filepath.Join(os.TempDir(), "geth-lumberjack.log"))
		}
		logOutputFile = &lumberjack.Logger{
			Filename:   globalLogFile,
			MaxSize:    int(globalLogMaxSize),
			MaxBackups: int(globalLogMaxBackups),
			MaxAge:     int(globalLogMaxAge),
			Compress:   globalLogCompress,
		}
		output = io.MultiWriter(terminalOutput, logOutputFile)
	} else if globalLogFile != "" {
		var err error
		if logOutputFile, err = os.OpenFile(globalLogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err != nil {
			return err
		}
		output = io.MultiWriter(logOutputFile, terminalOutput)
		context = append(context, "location", globalLogFile)
	} else {
		output = terminalOutput
	}

	var deprecatedLogJSONUsed bool
	if cmd.IsSet(logjsonFlag.Name) {
		deprecatedLogJSONUsed = true
		if cmd.Bool(logjsonFlag.Name) {
			globalLogFormat = "json"
		}
	}

	switch {
	case globalLogFormat == "json":
		handler = log.JSONHandler(output)
	case globalLogFormat == "logfmt":
		handler = log.LogfmtHandler(output)
	case globalLogFormat == "", globalLogFormat == "terminal":
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
		return fmt.Errorf("unknown log format: %v", globalLogFormat)
	}
	glogger = log.NewGlogHandler(handler)

	// logging
	verbosity := log.FromLegacyLevel(int(globalVerbosity))
	glogger.Verbosity(verbosity)
	if globalVmodule != "" {
		glogger.Vmodule(globalVmodule)
	}
	log.SetDefault(log.NewLogger(glogger))

	// Print deprecation notices. This needs to be done after logging is initialized.
	if deprecatedLogJSONUsed {
		log.Warn("Command-line flag --log.json is deprecated. Please use --log.format=json instead.")
	}
	if cmd.IsSet(vmoduleFlag.Name) {
		log.Warn("Command-line flag --vmodule is deprecated. Please use --log.vmodule instead.")
	}

	// profiling, tracing
	if globalBlockprofileRate >= 0 {
		Handler.SetBlockProfileRate(int(globalBlockprofileRate))
	}
	if globalGoTraceFile != "" {
		if err := Handler.StartGoTrace(globalGoTraceFile); err != nil {
			return err
		}
	}
	if globalCPUProfileFile != "" {
		if err := Handler.StartCPUProfile(globalCPUProfileFile); err != nil {
			return err
		}
	}

	// pprof server
	if globalPprofEnabled {
		address := net.JoinHostPort(globalPprofAddr, fmt.Sprintf("%d", globalPprofPort))
		// This context value ("metrics.addr") represents the utils.MetricsHTTPFlag.Name.
		// It cannot be imported because it will cause a cyclical dependency.
		//
		// TODO(fjl): move this to package metrics setup
		StartPProf(address, !cmd.IsSet("metrics.addr"))
	}
	if len(globalLogFile) > 0 || globalLogRotateEnabled {
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
