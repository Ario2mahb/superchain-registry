package superchain

import (
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"reflect"
	"strings"

	"golang.org/x/exp/maps"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

//go:embed configs
var superchainFS embed.FS

//go:embed extra/addresses extra/bytecodes extra/genesis extra/genesis-system-configs
var extraFS embed.FS

//go:embed implementations
var implementationsFS embed.FS

//go:embed semver.yaml
var semverFS embed.FS

type BlockID struct {
	Hash   Hash   `yaml:"hash"`
	Number uint64 `yaml:"number"`
}

type ChainGenesis struct {
	L1        BlockID   `yaml:"l1"`
	L2        BlockID   `yaml:"l2"`
	L2Time    uint64    `yaml:"l2_time"`
	ExtraData *HexBytes `yaml:"extra_data,omitempty"`
}

type ChainConfig struct {
	Name         string `yaml:"name"`
	ChainID      uint64 `yaml:"chain_id"`
	PublicRPC    string `yaml:"public_rpc"`
	SequencerRPC string `yaml:"sequencer_rpc"`
	Explorer     string `yaml:"explorer"`

	SystemConfigAddr Address `yaml:"system_config_addr"`
	BatchInboxAddr   Address `yaml:"batch_inbox_addr"`

	Genesis ChainGenesis `yaml:"genesis"`

	// Superchain is a simple string to identify the superchain.
	// This is implied by directory structure, and not encoded in the config file itself.
	Superchain string `yaml:"-"`
	// Chain is a simple string to identify the chain, within its superchain context.
	// This matches the resource filename, it is not encoded in the config file itself.
	Chain string `yaml:"-"`
}

// AddressList represents the set of network specific contracts for a given network.
type AddressList struct {
	AddressManager                    Address `json:"AddressManager"`
	L1CrossDomainMessengerProxy       Address `json:"L1CrossDomainMessengerProxy"`
	L1ERC721BridgeProxy               Address `json:"L1ERC721BridgeProxy"`
	L1StandardBridgeProxy             Address `json:"L1StandardBridgeProxy"`
	L2OutputOracleProxy               Address `json:"L2OutputOracleProxy"`
	OptimismMintableERC20FactoryProxy Address `json:"OptimismMintableERC20FactoryProxy"`
	OptimismPortalProxy               Address `json:"OptimismPortalProxy"`
	ProxyAdmin                        Address `json:"ProxyAdmin"`
}

// ImplementationList represents the set of implementation contracts to be used together
// for a network.
type ImplementationList struct {
	L1CrossDomainMessenger       VersionedContract `json:"L1CrossDomainMessenger"`
	L1ERC721Bridge               VersionedContract `json:"L1ERC721Bridge"`
	L1StandardBridge             VersionedContract `json:"L1StandardBridge"`
	L2OutputOracle               VersionedContract `json:"L2OutputOracle"`
	OptimismMintableERC20Factory VersionedContract `json:"OptimismMintableERC20Factory"`
	OptimismPortal               VersionedContract `json:"OptimismPortal"`
	SystemConfig                 VersionedContract `json:"SystemConfig"`
}

// ContractImplementations represent a set of contract implementations on a given network.
// The key in the map represents the semantic version of the contract and the value is the
// address that the contract is deployed to.
type ContractImplementations struct {
	L1CrossDomainMessenger       AddressSet `yaml:"l1_cross_domain_messenger"`
	L1ERC721Bridge               AddressSet `yaml:"l1_erc721_bridge"`
	L1StandardBridge             AddressSet `yaml:"l1_standard_bridge"`
	L2OutputOracle               AddressSet `yaml:"l2_output_oracle"`
	OptimismMintableERC20Factory AddressSet `yaml:"optimism_mintable_erc20_factory"`
	OptimismPortal               AddressSet `yaml:"optimism_portal"`
	SystemConfig                 AddressSet `yaml:"system_config"`
}

// AddressSet represents a set of addresses for a given
// contract. They are keyed by the semantic version.
type AddressSet map[string]Address

// VersionedContract represents a contract that has a semantic version.
type VersionedContract struct {
	Version string  `json:"version"`
	Address Address `json:"address"`
}

// Get will handle getting semantic versions from the set
// in the case where the semver string is not prefixed with
// a "v" as well as if it does have a "v" prefix.
func (a AddressSet) Get(key string) Address {
	if !strings.HasPrefix(key, "v") {
		key = "v" + key
	}
	if addr, ok := a[strings.TrimPrefix(key, "v")]; ok {
		return addr
	}
	return a[key]
}

// Versions will return the list of semantic versions for a contract.
// It handles the case where the versions are not prefixed with a "v".
func (a AddressSet) Versions() []string {
	keys := maps.Keys(a)
	for i, k := range keys {
		keys[i] = canonicalizeSemver(k)
	}
	semver.Sort(keys)
	return keys
}

// Resolve will return a set of addresses that resolve a given
// semantic version set.
func (c ContractImplementations) Resolve(versions ContractVersions) (ImplementationList, error) {
	var implementations ImplementationList
	var err error
	if implementations.L1CrossDomainMessenger, err = resolve(c.L1CrossDomainMessenger, versions.L1CrossDomainMessenger); err != nil {
		return implementations, fmt.Errorf("L1CrossDomainMessenger: %w", err)
	}
	if implementations.L1ERC721Bridge, err = resolve(c.L1ERC721Bridge, versions.L1ERC721Bridge); err != nil {
		return implementations, fmt.Errorf("L1ERC721Bridge: %w", err)
	}
	if implementations.L1StandardBridge, err = resolve(c.L1StandardBridge, versions.L1StandardBridge); err != nil {
		return implementations, fmt.Errorf("L1StandardBridge: %w", err)
	}
	if implementations.L2OutputOracle, err = resolve(c.L2OutputOracle, versions.L2OutputOracle); err != nil {
		return implementations, fmt.Errorf("L2OutputOracle: %w", err)
	}
	if implementations.OptimismMintableERC20Factory, err = resolve(c.OptimismMintableERC20Factory, versions.OptimismMintableERC20Factory); err != nil {
		return implementations, fmt.Errorf("OptimismMintableERC20Factory: %w", err)
	}
	if implementations.OptimismPortal, err = resolve(c.OptimismPortal, versions.OptimismPortal); err != nil {
		return implementations, fmt.Errorf("OptimismPortal: %w", err)
	}
	if implementations.SystemConfig, err = resolve(c.SystemConfig, versions.SystemConfig); err != nil {
		return implementations, fmt.Errorf("SystemConfig: %w", err)
	}
	return implementations, nil
}

// resolve returns a VersionedContract that matches the passed in semver version
// given a set of addresses.
func resolve(set AddressSet, version string) (VersionedContract, error) {
	version = canonicalizeSemver(version)

	var out VersionedContract
	keys := set.Versions()
	if len(keys) == 0 {
		return out, fmt.Errorf("no implementations found")
	}

	for _, k := range keys {
		res := semver.Compare(k, version)
		if res >= 0 {
			out = VersionedContract{
				Version: k,
				Address: set.Get(k),
			}
			if res == 0 {
				break
			}
		}
	}
	if out == (VersionedContract{}) {
		return out, fmt.Errorf("cannot resolve semver")
	}
	return out, nil
}

// ContractVersions represents the desired semantic version of the contracts
// in the superchain. This currently only supports L1 contracts but could
// represent L2 predeploys in the future.
type ContractVersions struct {
	L1CrossDomainMessenger       string `yaml:"l1_cross_domain_messenger"`
	L1ERC721Bridge               string `yaml:"l1_erc721_bridge"`
	L1StandardBridge             string `yaml:"l1_standard_bridge"`
	L2OutputOracle               string `yaml:"l2_output_oracle"`
	OptimismMintableERC20Factory string `yaml:"optimism_mintable_erc20_factory"`
	OptimismPortal               string `yaml:"optimism_portal"`
	SystemConfig                 string `yaml:"system_config"`
}

// Check will sanity check the validity of the semantic version strings
// in the ContractVersions struct.
func (c ContractVersions) Check() error {
	val := reflect.ValueOf(c)
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		str, ok := field.Interface().(string)
		if !ok {
			return fmt.Errorf("invalid type for field %s", val.Type().Field(i).Name)
		}
		if str == "" {
			return fmt.Errorf("empty version for field %s", val.Type().Field(i).Name)
		}
		str = canonicalizeSemver(str)
		if !semver.IsValid(str) {
			return fmt.Errorf("invalid semver %s for field %s", str, val.Type().Field(i).Name)
		}
	}
	return nil
}

// newContractImplementations returns a new empty ContractImplementations.
// Use this constructor to ensure that none of struct fields are nil.
// It will also merge the local network implementations into the global implementations
// because the global implementations were deployed with create2 and therefore should
// be on every network.
func newContractImplementations(network string) (ContractImplementations, error) {
	var globals ContractImplementations
	globalData, err := implementationsFS.ReadFile(path.Join("implementations", "implementations.yaml"))
	if err != nil {
		return globals, fmt.Errorf("failed to read implementations: %w", err)
	}
	if err := yaml.Unmarshal(globalData, &globals); err != nil {
		return globals, fmt.Errorf("failed to decode implementations: %w", err)
	}
	setAddressSetsIfNil(&globals)
	if network == "" {
		return globals, nil
	}

	filepath := path.Join("implementations", "networks", network+".yaml")
	var impls ContractImplementations
	data, err := implementationsFS.ReadFile(filepath)
	if err != nil {
		return impls, fmt.Errorf("failed to read implementations: %w", err)
	}
	if err := yaml.Unmarshal(data, &impls); err != nil {
		return impls, fmt.Errorf("failed to decode implementations: %w", err)
	}
	setAddressSetsIfNil(&impls)
	globals.Merge(impls)

	return globals, nil
}

// setAddressSetsIfNil will ensure that all of the struct values on a
// ContractImplementations struct are non nil.
func setAddressSetsIfNil(impls *ContractImplementations) {
	if impls.L1CrossDomainMessenger == nil {
		impls.L1CrossDomainMessenger = make(AddressSet)
	}
	if impls.L1ERC721Bridge == nil {
		impls.L1ERC721Bridge = make(AddressSet)
	}
	if impls.L1StandardBridge == nil {
		impls.L1StandardBridge = make(AddressSet)
	}
	if impls.L2OutputOracle == nil {
		impls.L2OutputOracle = make(AddressSet)
	}
	if impls.OptimismMintableERC20Factory == nil {
		impls.OptimismMintableERC20Factory = make(AddressSet)
	}
	if impls.OptimismPortal == nil {
		impls.OptimismPortal = make(AddressSet)
	}
	if impls.SystemConfig == nil {
		impls.SystemConfig = make(AddressSet)
	}
}

// copySemverMap is a concrete implementation of maps.Copy for map[string]Address.
var copySemverMap = maps.Copy[map[string]Address, map[string]Address]

// canonicalizeSemver will ensure that the version string has a "v" prefix.
// This is because the semver library being used requires the "v" prefix,
// even though
func canonicalizeSemver(version string) string {
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return version
}

// Merge will combine two ContractImplementations into one. Any conflicting keys will
// be overwritten by the arguments. It assumes that nonce of the struct fields are nil.
func (c ContractImplementations) Merge(other ContractImplementations) {
	copySemverMap(c.L1CrossDomainMessenger, other.L1CrossDomainMessenger)
	copySemverMap(c.L1ERC721Bridge, other.L1ERC721Bridge)
	copySemverMap(c.L1StandardBridge, other.L1StandardBridge)
	copySemverMap(c.L2OutputOracle, other.L2OutputOracle)
	copySemverMap(c.OptimismMintableERC20Factory, other.OptimismMintableERC20Factory)
	copySemverMap(c.OptimismPortal, other.OptimismPortal)
	copySemverMap(c.SystemConfig, other.SystemConfig)
}

// Copy will return a shallow copy of the ContractImplementations.
func (c ContractImplementations) Copy() ContractImplementations {
	return ContractImplementations{
		L1CrossDomainMessenger:       maps.Clone(c.L1CrossDomainMessenger),
		L1ERC721Bridge:               maps.Clone(c.L1ERC721Bridge),
		L1StandardBridge:             maps.Clone(c.L1StandardBridge),
		L2OutputOracle:               maps.Clone(c.L2OutputOracle),
		OptimismMintableERC20Factory: maps.Clone(c.OptimismMintableERC20Factory),
		OptimismPortal:               maps.Clone(c.OptimismPortal),
		SystemConfig:                 maps.Clone(c.SystemConfig),
	}
}

type GenesisSystemConfig struct {
	BatcherAddr Address `json:"batcherAddr"`
	Overhead    Hash    `json:"overhead"`
	Scalar      Hash    `json:"scalar"`
	GasLimit    uint64  `json:"gasLimit"`
}

type GenesisAccount struct {
	CodeHash Hash          `json:"codeHash,omitempty"` // code hash only, to reduce overhead of duplicate bytecode
	Storage  map[Hash]Hash `json:"storage,omitempty"`
	Balance  *HexBig       `json:"balance,omitempty"`
	Nonce    uint64        `json:"nonce,omitempty"`
}

type Genesis struct {
	// Block properties
	Nonce      uint64  `json:"nonce"`
	Timestamp  uint64  `json:"timestamp"`
	ExtraData  []byte  `json:"extraData"`
	GasLimit   uint64  `json:"gasLimit"`
	Difficulty *HexBig `json:"difficulty"`
	Mixhash    Hash    `json:"mixHash"`
	Coinbase   Address `json:"coinbase"`
	Number     uint64  `json:"number"`
	GasUsed    uint64  `json:"gasUsed"`
	ParentHash Hash    `json:"parentHash"`
	BaseFee    *HexBig `json:"baseFeePerGas"`
	// State data
	Alloc map[Address]GenesisAccount `json:"alloc"`
	// StateHash substitutes for a full embedded state allocation,
	// for instantiating states with the genesis block only, to be state-synced before operation.
	// Archive nodes should use a full external genesis.json or datadir.
	StateHash *Hash `json:"stateHash,omitempty"`
	// The chain-config is not included. This is derived from the chain and superchain definition instead.
}

type SuperchainL1Info struct {
	ChainID   uint64 `yaml:"chain_id"`
	PublicRPC string `yaml:"public_rpc"`
	Explorer  string `yaml:"explorer"`
}

type SuperchainConfig struct {
	Name string           `yaml:"name"`
	L1   SuperchainL1Info `yaml:"l1"`

	ProtocolVersionsAddr *Address `yaml:"protocol_versions_addr,omitempty"`
	SuperchainConfigAddr *Address `yaml:"superchain_config_addr,omitempty"`

	// Hardfork Configuration
	CanyonTime  *uint64 `yaml:"canyon_time,omitempty"`
	DeltaTime   *uint64 `yaml:"delta_time,omitempty"`
	EclipseTime *uint64 `yaml:"eclipse_time,omitempty"`
	FjordTime   *uint64 `yaml:"fjord_time,omitempty"`
}

type Superchain struct {
	Config SuperchainConfig

	// Chains that are part of this superchain
	ChainIDs []uint64

	// Superchain identifier, without capitalization or display changes.
	Superchain string
}

var Superchains = map[string]*Superchain{}

var OPChains = map[uint64]*ChainConfig{}

var Addresses = map[uint64]*AddressList{}

var GenesisSystemConfigs = map[uint64]*GenesisSystemConfig{}

// Implementations represents a global mapping of contract implementations
// to chain by chain id.
var Implementations = map[uint64]ContractImplementations{}

// SuperchainSemver represents a global mapping of contract name to desired semver version.
var SuperchainSemver ContractVersions

func init() {
	var err error
	SuperchainSemver, err = newContractVersions()
	if err != nil {
		panic(fmt.Errorf("failed to read semver.yaml: %w", err))
	}

	superchainTargets, err := superchainFS.ReadDir("configs")
	if err != nil {
		panic(fmt.Errorf("failed to read superchain dir: %w", err))
	}
	// iterate over superchain-target entries
	for _, s := range superchainTargets {
		if !s.IsDir() {
			continue // ignore files, e.g. a readme
		}
		// Load superchain-target config
		superchainConfigData, err := superchainFS.ReadFile(path.Join("configs", s.Name(), "superchain.yaml"))
		if err != nil {
			panic(fmt.Errorf("failed to read superchain config: %w", err))
		}
		var superchainEntry Superchain
		if err := yaml.Unmarshal(superchainConfigData, &superchainEntry.Config); err != nil {
			panic(fmt.Errorf("failed to decode superchain config: %w", err))
		}
		superchainEntry.Superchain = s.Name()

		// iterate over the chains of this superchain-target
		chainEntries, err := superchainFS.ReadDir(path.Join("configs", s.Name()))
		if err != nil {
			panic(fmt.Errorf("failed to read superchain dir: %w", err))
		}
		for _, c := range chainEntries {
			if c.IsDir() || !strings.HasSuffix(c.Name(), ".yaml") {
				continue // ignore files. Chains must be a directory of configs.
			}
			if c.Name() == "superchain.yaml" {
				continue // already processed
			}
			// load chain config
			chainConfigData, err := superchainFS.ReadFile(path.Join("configs", s.Name(), c.Name()))
			if err != nil {
				panic(fmt.Errorf("failed to read superchain config %s/%s: %w", s.Name(), c.Name(), err))
			}
			var chainConfig ChainConfig
			if err := yaml.Unmarshal(chainConfigData, &chainConfig); err != nil {
				panic(fmt.Errorf("failed to decode chain config %s/%s: %w", s.Name(), c.Name(), err))
			}
			chainConfig.Chain = strings.TrimSuffix(c.Name(), ".yaml")

			jsonName := chainConfig.Chain + ".json"
			addressesData, err := extraFS.ReadFile(path.Join("extra", "addresses", s.Name(), jsonName))
			if err != nil {
				panic(fmt.Errorf("failed to read addresses data of chain %s/%s: %w", s.Name(), jsonName, err))
			}
			var addrs AddressList
			if err := json.Unmarshal(addressesData, &addrs); err != nil {
				panic(fmt.Errorf("failed to decode addresses %s/%s: %w", s.Name(), jsonName, err))
			}

			genesisSysCfgData, err := extraFS.ReadFile(path.Join("extra", "genesis-system-configs", s.Name(), jsonName))
			if err != nil {
				panic(fmt.Errorf("failed to read genesis system config data of chain %s/%s: %w", s.Name(), jsonName, err))
			}
			var genesisSysCfg GenesisSystemConfig
			if err := json.Unmarshal(genesisSysCfgData, &genesisSysCfg); err != nil {
				panic(fmt.Errorf("failed to decode genesis system config %s/%s: %w", s.Name(), jsonName, err))
			}

			chainConfig.Superchain = s.Name()
			if other, ok := OPChains[chainConfig.ChainID]; ok {
				panic(fmt.Errorf("found chain config %q in superchain target %q with chain ID %d "+
					"conflicts with chain %q in superchain %q and chain ID %d",
					chainConfig.Name, chainConfig.Superchain, chainConfig.ChainID,
					other.Name, other.Superchain, other.ChainID))
			}
			superchainEntry.ChainIDs = append(superchainEntry.ChainIDs, chainConfig.ChainID)
			OPChains[chainConfig.ChainID] = &chainConfig
			Addresses[chainConfig.ChainID] = &addrs
			GenesisSystemConfigs[chainConfig.ChainID] = &genesisSysCfg
		}

		Superchains[superchainEntry.Superchain] = &superchainEntry

		implementations, err := newContractImplementations(s.Name())
		if err != nil {
			panic(fmt.Errorf("failed to read implementations of superchain target %s: %w", s.Name(), err))
		}

		Implementations[superchainEntry.Config.L1.ChainID] = implementations
	}
}

// newContractVersions will read the contract versions from semver.yaml
// and check to make sure that it is valid.
func newContractVersions() (ContractVersions, error) {
	var versions ContractVersions
	semvers, err := semverFS.ReadFile("semver.yaml")
	if err != nil {
		return versions, fmt.Errorf("failed to read semver.yaml: %w", err)
	}
	if err := yaml.Unmarshal(semvers, &versions); err != nil {
		return versions, fmt.Errorf("failed to unmarshal semver.yaml: %w", err)
	}
	if err := versions.Check(); err != nil {
		return versions, fmt.Errorf("semver.yaml is invalid: %w", err)
	}
	return versions, nil
}

func LoadGenesis(chainID uint64) (*Genesis, error) {
	ch, ok := OPChains[chainID]
	if !ok {
		return nil, fmt.Errorf("unknown chain %d", chainID)
	}
	f, err := extraFS.Open(path.Join("extra", "genesis", ch.Superchain, ch.Chain+".json.gz"))
	if err != nil {
		return nil, fmt.Errorf("failed to open chain genesis definition of %d: %w", chainID, err)
	}
	defer f.Close()
	r, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("failed to open gzip reader of genesis data of %d: %w", chainID, err)
	}
	defer r.Close()
	var out Genesis
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode genesis allocation of %d: %w", chainID, err)
	}
	return &out, nil
}

func LoadContractBytecode(codeHash Hash) ([]byte, error) {
	f, err := extraFS.Open(path.Join("extra", "bytecodes", codeHash.String()+".bin.gz"))
	if err != nil {
		return nil, fmt.Errorf("failed to open bytecode %s: %w", codeHash, err)
	}
	defer f.Close()
	r, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("")
	}
	defer r.Close()
	return io.ReadAll(r)
}
