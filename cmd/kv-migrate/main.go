package main

import (
	"fmt"
	"io/ioutil"
	"os"

	"github.com/alecthomas/kong"
	"github.com/fystack/transaction-indexer/pkg/common/config"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/fystack/transaction-indexer/pkg/kvstore"
	yaml "github.com/goccy/go-yaml"
	"github.com/hashicorp/consul/api"
)

type endpoint string

const (
	epBadger endpoint = "badger"
	epConsul endpoint = "consul"
)

type CLI struct {
	From string `help:"source backend" enum:"badger,consul" default:"badger"`
	To   string `help:"destination backend" enum:"badger,consul" default:"consul"`

	Prefixes []string `help:"key prefixes to include (repeatable)" short:"p"`
	DryRun   bool     `help:"print actions without writing"`
	Verify   bool     `help:"verify migrated values match" default:"true"`

	SrcConfig string `name:"src-config" help:"YAML config file containing kvstore for source"`
	DstConfig string `name:"dst-config" help:"YAML config file containing kvstore for destination"`

	BadgerSrcDir    string `name:"badger-src-dir" help:"source badger directory"`
	BadgerSrcPrefix string `name:"badger-src-prefix" help:"source badger prefix"`
	BadgerDstDir    string `name:"badger-dst-dir" help:"destination badger directory"`
	BadgerDstPrefix string `name:"badger-dst-prefix" help:"destination badger prefix"`

	ConsulSrcAddr   string `name:"consul-src-address" help:"source consul address host:port"`
	ConsulSrcScheme string `name:"consul-src-scheme" help:"source consul scheme" default:"http"`
	ConsulSrcFolder string `name:"consul-src-folder" help:"source consul folder (prefix)"`
	ConsulSrcToken  string `name:"consul-src-token" help:"source consul token"`
	ConsulSrcUser   string `name:"consul-src-user" help:"source consul http auth user"`
	ConsulSrcPass   string `name:"consul-src-pass" help:"source consul http auth password"`

	ConsulDstAddr   string `name:"consul-dst-address" help:"destination consul address host:port"`
	ConsulDstScheme string `name:"consul-dst-scheme" help:"destination consul scheme" default:"http"`
	ConsulDstFolder string `name:"consul-dst-folder" help:"destination consul folder (prefix)"`
	ConsulDstToken  string `name:"consul-dst-token" help:"destination consul token"`
	ConsulDstUser   string `name:"consul-dst-user" help:"destination consul http auth user"`
	ConsulDstPass   string `name:"consul-dst-pass" help:"destination consul http auth password"`
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli, kong.Name("kv-migrate"), kong.Description("Migrate keys between Badger and Consul"))

	if len(cli.Prefixes) == 0 {
		ctx.FatalIfErrorf(fmt.Errorf("at least one --prefix is required (Badger requires non-empty prefix)"))
	}

	// build source store (optionally from config file)
	fromEp := endpoint(cli.From)
	srcSide := buildSide{
		badgerDir: cli.BadgerSrcDir, badgerPrefix: cli.BadgerSrcPrefix,
		consulAddr: cli.ConsulSrcAddr, consulScheme: cli.ConsulSrcScheme, consulFolder: cli.ConsulSrcFolder,
		consulToken: cli.ConsulSrcToken, consulUser: cli.ConsulSrcUser, consulPass: cli.ConsulSrcPass,
	}
	if cli.SrcConfig != "" {
		kvCfg, err := loadKVStoreCfg(cli.SrcConfig)
		ctx.FatalIfErrorf(err)
		fromEp = endpoint(kvCfg.Type)
		applyKVToSide(&srcSide, kvCfg)
	}
	src, err := buildStore(fromEp, srcSide)
	ctx.FatalIfErrorf(err)
	defer src.Close()

	// build destination store (optionally from config file)
	toEp := endpoint(cli.To)
	dstSide := buildSide{
		badgerDir: cli.BadgerDstDir, badgerPrefix: cli.BadgerDstPrefix,
		consulAddr: cli.ConsulDstAddr, consulScheme: cli.ConsulDstScheme, consulFolder: cli.ConsulDstFolder,
		consulToken: cli.ConsulDstToken, consulUser: cli.ConsulDstUser, consulPass: cli.ConsulDstPass,
	}
	if cli.DstConfig != "" {
		kvCfg, err := loadKVStoreCfg(cli.DstConfig)
		ctx.FatalIfErrorf(err)
		toEp = endpoint(kvCfg.Type)
		applyKVToSide(&dstSide, kvCfg)
	}
	dst, err := buildStore(toEp, dstSide)
	ctx.FatalIfErrorf(err)
	defer dst.Close()

	total, copied, skipped, err := migrate(src, dst, cli.Prefixes, cli.DryRun, cli.Verify)
	ctx.FatalIfErrorf(err)
	fmt.Fprintf(os.Stdout, "done: total=%d copied=%d skipped=%d\n", total, copied, skipped)
}

type buildSide struct {
	badgerDir    string
	badgerPrefix string

	consulScheme string
	consulAddr   string
	consulFolder string
	consulToken  string
	consulUser   string
	consulPass   string
}

func buildStore(ep endpoint, side buildSide) (infra.KVStore, error) {
	switch ep {
	case epBadger:
		return kvstore.NewBadgerStore(side.badgerDir, side.badgerPrefix, infra.JSON)
	case epConsul:
		var httpAuth *api.HttpBasicAuth
		if side.consulUser != "" || side.consulPass != "" {
			httpAuth = &api.HttpBasicAuth{Username: side.consulUser, Password: side.consulPass}
		}
		return kvstore.NewConsulClient(kvstore.Options{
			Scheme:   side.consulScheme,
			Address:  side.consulAddr,
			Folder:   side.consulFolder,
			Codec:    infra.JSON,
			Token:    side.consulToken,
			HttpAuth: httpAuth,
		})
	default:
		return nil, fmt.Errorf("unsupported endpoint: %s", ep)
	}
}

func migrate(src infra.KVStore, dst infra.KVStore, prefixes []string, dryRun bool, verify bool) (int, int, int, error) {
	var pairs []*infra.KVPair
	for _, p := range prefixes {
		items, err := src.List(p)
		if err != nil {
			return 0, 0, 0, err
		}
		pairs = append(pairs, items...)
	}

	total := len(pairs)
	copied := 0
	skipped := 0

	for _, kv := range pairs {
		key := kv.Key
		val := string(kv.Value)
		if dryRun {
			fmt.Printf("copy %s\n", key)
			continue
		}
		if err := dst.Set(key, val); err != nil {
			return total, copied, skipped, fmt.Errorf("set %s: %w", key, err)
		}
		copied++
		if verify {
			got, err := dst.Get(key)
			if err != nil {
				return total, copied, skipped, fmt.Errorf("verify get %s: %w", key, err)
			}
			if got != val {
				return total, copied, skipped, fmt.Errorf("verify mismatch for %s", key)
			}
		}
	}

	return total, copied, skipped, nil
}

func loadKVStoreCfg(path string) (config.KVStoreCfg, error) {
	var cfgFile struct {
		KVStore config.KVStoreCfg `yaml:"kvstore"`
	}
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return config.KVStoreCfg{}, err
	}
	if err := yaml.Unmarshal(b, &cfgFile); err != nil {
		return config.KVStoreCfg{}, err
	}
	return cfgFile.KVStore, nil
}

func applyKVToSide(dst *buildSide, kv config.KVStoreCfg) {
	switch kv.Type {
	case "badger":
		if dst.badgerDir == "" {
			dst.badgerDir = kv.Badger.Directory
		}
		if dst.badgerPrefix == "" {
			dst.badgerPrefix = kv.Badger.Prefix
		}
	case "consul":
		if dst.consulScheme == "" {
			dst.consulScheme = kv.Consul.Scheme
		}
		if dst.consulAddr == "" {
			dst.consulAddr = kv.Consul.Address
		}
		if dst.consulFolder == "" {
			dst.consulFolder = kv.Consul.Folder
		}
		if dst.consulToken == "" {
			dst.consulToken = kv.Consul.Token
		}
	}
}

/*
pp config to consul
kv-migrate --src-config configs/config.yaml --to consul \
  --consul-dst-address 127.0.0.1:8500 --consul-dst-folder indexer \
  -p latest_block_ -p catchup_progress_


  // badger to consul
  kv-migrate --from badger --to consul \
  --consul-dst-address 127.0.0.1:8500 --consul-dst-folder indexer \
  -p latest_block_ -p catchup_progress_


*/
