// Copyright 2017 The go-ethereum Authors
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
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"unicode"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/external"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/accounts/scwallet"
	"github.com/ethereum/go-ethereum/accounts/usbwallet"
	"github.com/ethereum/go-ethereum/beacon/blsync"
	"github.com/ethereum/go-ethereum/cmd/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/eth/catalyst"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/internal/flags"
	"github.com/ethereum/go-ethereum/internal/version"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/naoina/toml"
	"github.com/urfave/cli/v3"
)

var (
	dumpConfigCommand = &cli.Command{
		Action:      dumpConfig,
		Name:        "dumpconfig",
		Usage:       "Export configuration values in a TOML format",
		ArgsUsage:   "<dumpfile (optional)>",
		Flags:       slices.Concat(nodeFlags, rpcFlags),
		Description: `Export configuration values in TOML format (to stdout by default).`,
	}

	configFileFlag = &cli.StringFlag{
		Name:     "config",
		Usage:    "TOML configuration file",
		Category: flags.EthCategory,
	}
)

// These settings ensure that TOML keys use the same names as Go struct fields.
var tomlSettings = toml.Config{
	NormFieldName: func(rt reflect.Type, key string) string {
		return key
	},
	FieldToKey: func(rt reflect.Type, field string) string {
		return field
	},
	MissingField: func(rt reflect.Type, field string) error {
		id := fmt.Sprintf("%s.%s", rt.String(), field)
		if deprecatedConfigFields[id] {
			log.Warn(fmt.Sprintf("Config field '%s' is deprecated and won't have any effect.", id))
			return nil
		}
		var link string
		if unicode.IsUpper(rune(rt.Name()[0])) && rt.PkgPath() != "main" {
			link = fmt.Sprintf(", see https://godoc.org/%s#%s for available fields", rt.PkgPath(), rt.Name())
		}
		return fmt.Errorf("field '%s' is not defined in %s%s", field, rt.String(), link)
	},
}

var deprecatedConfigFields = map[string]bool{
	"ethconfig.Config.EVMInterpreter":          true,
	"ethconfig.Config.EWASMInterpreter":        true,
	"ethconfig.Config.TrieCleanCacheJournal":   true,
	"ethconfig.Config.TrieCleanCacheRejournal": true,
	"ethconfig.Config.LightServ":               true,
	"ethconfig.Config.LightIngress":            true,
	"ethconfig.Config.LightEgress":             true,
	"ethconfig.Config.LightPeers":              true,
	"ethconfig.Config.LightNoPrune":            true,
	"ethconfig.Config.LightNoSyncServe":        true,
}

type ethstatsConfig struct {
	URL string `toml:",omitempty"`
}

type gethConfig struct {
	Eth      ethconfig.Config
	Node     node.Config
	Ethstats ethstatsConfig
	Metrics  metrics.Config
}

func loadConfig(file string, cfg *gethConfig) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	err = tomlSettings.NewDecoder(bufio.NewReader(f)).Decode(cfg)
	// Add file name to errors that have a line number.
	if _, ok := err.(*toml.LineError); ok {
		err = errors.New(file + ", " + err.Error())
	}
	return err
}

func defaultNodeConfig() node.Config {
	git, _ := version.VCS()
	cfg := node.DefaultConfig
	cfg.Name = clientIdentifier
	cfg.Version = version.WithCommit(git.Commit, git.Date)
	cfg.HTTPModules = append(cfg.HTTPModules, "eth")
	cfg.WSModules = append(cfg.WSModules, "eth")
	cfg.IPCPath = clientIdentifier + ".ipc"
	return cfg
}

// loadBaseConfig loads the gethConfig based on the given command line
// parameters and config file.
func loadBaseConfig(cmd *cli.Command) gethConfig {
	// Load defaults.
	cfg := gethConfig{
		Eth:     ethconfig.Defaults,
		Node:    defaultNodeConfig(),
		Metrics: metrics.DefaultConfig,
	}

	// Load config file.
	if file := cmd.String(configFileFlag.Name); file != "" {
		if err := loadConfig(file, &cfg); err != nil {
			utils.Fatalf("%v", err)
		}
	}

	// Apply flags.
	utils.SetNodeConfig(cmd, &cfg.Node)
	return cfg
}

// makeConfigNode loads geth configuration and creates a blank node instance.
func makeConfigNode(cmd *cli.Command) (*node.Node, gethConfig) {
	cfg := loadBaseConfig(cmd)
	stack, err := node.New(&cfg.Node)
	if err != nil {
		utils.Fatalf("Failed to create the protocol stack: %v", err)
	}
	// Node doesn't by default populate account manager backends
	if err := setAccountManagerBackends(stack.Config(), stack.AccountManager(), stack.KeyStoreDir()); err != nil {
		utils.Fatalf("Failed to set account manager backends: %v", err)
	}

	utils.SetEthConfig(cmd, stack, &cfg.Eth)
	if cmd.IsSet(utils.EthStatsURLFlag.Name) {
		cfg.Ethstats.URL = cmd.String(utils.EthStatsURLFlag.Name)
	}
	applyMetricConfig(cmd, &cfg)

	return stack, cfg
}

// makeFullNode loads geth configuration and creates the Ethereum backend.
func makeFullNode(cmd *cli.Command) *node.Node {
	stack, cfg := makeConfigNode(cmd)
	if cmd.IsSet(utils.OverrideCancun.Name) {
		v := cmd.Uint(utils.OverrideCancun.Name)
		cfg.Eth.OverrideCancun = &v
	}
	if cmd.IsSet(utils.OverrideVerkle.Name) {
		v := cmd.Uint(utils.OverrideVerkle.Name)
		cfg.Eth.OverrideVerkle = &v
	}

	// Start metrics export if enabled
	utils.SetupMetrics(&cfg.Metrics)

	backend, eth := utils.RegisterEthService(stack, &cfg.Eth)

	// Create gauge with geth system and build information
	if eth != nil { // The 'eth' backend may be nil in light mode
		var protos []string
		for _, p := range eth.Protocols() {
			protos = append(protos, fmt.Sprintf("%v/%d", p.Name, p.Version))
		}
		metrics.NewRegisteredGaugeInfo("geth/info", nil).Update(metrics.GaugeInfoValue{
			"arch":      runtime.GOARCH,
			"os":        runtime.GOOS,
			"version":   cfg.Node.Version,
			"protocols": strings.Join(protos, ","),
		})
	}

	// Configure log filter RPC API.
	filterSystem := utils.RegisterFilterAPI(stack, backend, &cfg.Eth)

	// Configure GraphQL if requested.
	if cmd.IsSet(utils.GraphQLEnabledFlag.Name) {
		utils.RegisterGraphQLService(stack, backend, filterSystem, &cfg.Node)
	}
	// Add the Ethereum Stats daemon if requested.
	if cfg.Ethstats.URL != "" {
		utils.RegisterEthStatsService(stack, backend, cfg.Ethstats.URL)
	}
	// Configure full-sync tester service if requested
	if cmd.IsSet(utils.SyncTargetFlag.Name) {
		hex := hexutil.MustDecode(cmd.String(utils.SyncTargetFlag.Name))
		if len(hex) != common.HashLength {
			utils.Fatalf("invalid sync target length: have %d, want %d", len(hex), common.HashLength)
		}
		utils.RegisterFullSyncTester(stack, eth, common.BytesToHash(hex))
	}

	if cmd.IsSet(utils.DeveloperFlag.Name) {
		// Start dev mode.
		simBeacon, err := catalyst.NewSimulatedBeacon(cmd.Uint(utils.DeveloperPeriodFlag.Name), eth)
		if err != nil {
			utils.Fatalf("failed to register dev mode catalyst service: %v", err)
		}
		catalyst.RegisterSimulatedBeaconAPIs(stack, simBeacon)
		stack.RegisterLifecycle(simBeacon)
	} else if cmd.IsSet(utils.BeaconApiFlag.Name) {
		// Start blsync mode.
		srv := rpc.NewServer()
		srv.RegisterName("engine", catalyst.NewConsensusAPI(eth))
		blsyncer := blsync.NewClient(utils.MakeBeaconLightConfig(cmd))
		blsyncer.SetEngineRPC(rpc.DialInProc(srv))
		stack.RegisterLifecycle(blsyncer)
	} else {
		// Launch the engine API for interacting with external consensus client.
		err := catalyst.Register(stack, eth)
		if err != nil {
			utils.Fatalf("failed to register catalyst service: %v", err)
		}
	}
	return stack
}

// dumpConfig is the dumpconfig command.
func dumpConfig(_ context.Context, ctx *cli.Command) error {
	_, cfg := makeConfigNode(ctx)
	comment := ""

	if cfg.Eth.Genesis != nil {
		cfg.Eth.Genesis = nil
		comment += "# Note: this config doesn't contain the genesis block.\n\n"
	}

	out, err := tomlSettings.Marshal(&cfg)
	if err != nil {
		return err
	}

	dump := os.Stdout
	if ctx.NArg() > 0 {
		dump, err = os.OpenFile(ctx.Args().Get(0), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return err
		}
		defer dump.Close()
	}
	dump.WriteString(comment)
	dump.Write(out)

	return nil
}

func applyMetricConfig(cmd *cli.Command, cfg *gethConfig) {
	if cmd.IsSet(utils.MetricsEnabledFlag.Name) {
		cfg.Metrics.Enabled = cmd.Bool(utils.MetricsEnabledFlag.Name)
	}
	if cmd.IsSet(utils.MetricsEnabledExpensiveFlag.Name) {
		log.Warn("Expensive metrics are collected by default, please remove this flag", "flag", utils.MetricsEnabledExpensiveFlag.Name)
	}
	if cmd.IsSet(utils.MetricsHTTPFlag.Name) {
		cfg.Metrics.HTTP = cmd.String(utils.MetricsHTTPFlag.Name)
	}
	if cmd.IsSet(utils.MetricsPortFlag.Name) {
		cfg.Metrics.Port = int(cmd.Int(utils.MetricsPortFlag.Name))
	}
	if cmd.IsSet(utils.MetricsEnableInfluxDBFlag.Name) {
		cfg.Metrics.EnableInfluxDB = cmd.Bool(utils.MetricsEnableInfluxDBFlag.Name)
	}
	if cmd.IsSet(utils.MetricsInfluxDBEndpointFlag.Name) {
		cfg.Metrics.InfluxDBEndpoint = cmd.String(utils.MetricsInfluxDBEndpointFlag.Name)
	}
	if cmd.IsSet(utils.MetricsInfluxDBDatabaseFlag.Name) {
		cfg.Metrics.InfluxDBDatabase = cmd.String(utils.MetricsInfluxDBDatabaseFlag.Name)
	}
	if cmd.IsSet(utils.MetricsInfluxDBUsernameFlag.Name) {
		cfg.Metrics.InfluxDBUsername = cmd.String(utils.MetricsInfluxDBUsernameFlag.Name)
	}
	if cmd.IsSet(utils.MetricsInfluxDBPasswordFlag.Name) {
		cfg.Metrics.InfluxDBPassword = cmd.String(utils.MetricsInfluxDBPasswordFlag.Name)
	}
	if cmd.IsSet(utils.MetricsInfluxDBTagsFlag.Name) {
		cfg.Metrics.InfluxDBTags = cmd.String(utils.MetricsInfluxDBTagsFlag.Name)
	}
	if cmd.IsSet(utils.MetricsEnableInfluxDBV2Flag.Name) {
		cfg.Metrics.EnableInfluxDBV2 = cmd.Bool(utils.MetricsEnableInfluxDBV2Flag.Name)
	}
	if cmd.IsSet(utils.MetricsInfluxDBTokenFlag.Name) {
		cfg.Metrics.InfluxDBToken = cmd.String(utils.MetricsInfluxDBTokenFlag.Name)
	}
	if cmd.IsSet(utils.MetricsInfluxDBBucketFlag.Name) {
		cfg.Metrics.InfluxDBBucket = cmd.String(utils.MetricsInfluxDBBucketFlag.Name)
	}
	if cmd.IsSet(utils.MetricsInfluxDBOrganizationFlag.Name) {
		cfg.Metrics.InfluxDBOrganization = cmd.String(utils.MetricsInfluxDBOrganizationFlag.Name)
	}
	// Sanity-check the commandline flags. It is fine if some unused fields is part
	// of the toml-config, but we expect the commandline to only contain relevant
	// arguments, otherwise it indicates an error.
	var (
		enableExport   = cmd.Bool(utils.MetricsEnableInfluxDBFlag.Name)
		enableExportV2 = cmd.Bool(utils.MetricsEnableInfluxDBV2Flag.Name)
	)
	if enableExport || enableExportV2 {
		v1FlagIsSet := cmd.IsSet(utils.MetricsInfluxDBUsernameFlag.Name) ||
			cmd.IsSet(utils.MetricsInfluxDBPasswordFlag.Name)

		v2FlagIsSet := cmd.IsSet(utils.MetricsInfluxDBTokenFlag.Name) ||
			cmd.IsSet(utils.MetricsInfluxDBOrganizationFlag.Name) ||
			cmd.IsSet(utils.MetricsInfluxDBBucketFlag.Name)

		if enableExport && v2FlagIsSet {
			utils.Fatalf("Flags --influxdb.metrics.organization, --influxdb.metrics.token, --influxdb.metrics.bucket are only available for influxdb-v2")
		} else if enableExportV2 && v1FlagIsSet {
			utils.Fatalf("Flags --influxdb.metrics.username, --influxdb.metrics.password are only available for influxdb-v1")
		}
	}
}

func setAccountManagerBackends(conf *node.Config, am *accounts.Manager, keydir string) error {
	scryptN := keystore.StandardScryptN
	scryptP := keystore.StandardScryptP
	if conf.UseLightweightKDF {
		scryptN = keystore.LightScryptN
		scryptP = keystore.LightScryptP
	}

	// Assemble the supported backends
	if len(conf.ExternalSigner) > 0 {
		log.Info("Using external signer", "url", conf.ExternalSigner)
		if extBackend, err := external.NewExternalBackend(conf.ExternalSigner); err == nil {
			am.AddBackend(extBackend)
			return nil
		} else {
			return fmt.Errorf("error connecting to external signer: %v", err)
		}
	}

	// For now, we're using EITHER external signer OR local signers.
	// If/when we implement some form of lockfile for USB and keystore wallets,
	// we can have both, but it's very confusing for the user to see the same
	// accounts in both externally and locally, plus very racey.
	am.AddBackend(keystore.NewKeyStore(keydir, scryptN, scryptP))
	if conf.USB {
		// Start a USB hub for Ledger hardware wallets
		if ledgerhub, err := usbwallet.NewLedgerHub(); err != nil {
			log.Warn(fmt.Sprintf("Failed to start Ledger hub, disabling: %v", err))
		} else {
			am.AddBackend(ledgerhub)
		}
		// Start a USB hub for Trezor hardware wallets (HID version)
		if trezorhub, err := usbwallet.NewTrezorHubWithHID(); err != nil {
			log.Warn(fmt.Sprintf("Failed to start HID Trezor hub, disabling: %v", err))
		} else {
			am.AddBackend(trezorhub)
		}
		// Start a USB hub for Trezor hardware wallets (WebUSB version)
		if trezorhub, err := usbwallet.NewTrezorHubWithWebUSB(); err != nil {
			log.Warn(fmt.Sprintf("Failed to start WebUSB Trezor hub, disabling: %v", err))
		} else {
			am.AddBackend(trezorhub)
		}
	}
	if len(conf.SmartCardDaemonPath) > 0 {
		// Start a smart card hub
		if schub, err := scwallet.NewHub(conf.SmartCardDaemonPath, scwallet.Scheme, keydir); err != nil {
			log.Warn(fmt.Sprintf("Failed to start smart card hub, disabling: %v", err))
		} else {
			am.AddBackend(schub)
		}
	}

	return nil
}
