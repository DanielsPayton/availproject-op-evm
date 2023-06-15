package tests

import (
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/command/server/config"
	"github.com/0xPolygon/polygon-edge/network"
	"github.com/0xPolygon/polygon-edge/secrets/helper"
	edge_server "github.com/0xPolygon/polygon-edge/server"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	consensus "github.com/maticnetwork/avail-settlement/consensus/avail"
	"github.com/maticnetwork/avail-settlement/pkg/avail"
	"github.com/maticnetwork/avail-settlement/pkg/common"
	pkg_config "github.com/maticnetwork/avail-settlement/pkg/config"
	"github.com/maticnetwork/avail-settlement/server"
)

const (
	testAccountPath = "../data/test-accounts"
)

type Context struct {
	servers     []instance
	jsonRPCURLs []*url.URL
}

type instance struct {
	nodeType    consensus.MechanismType
	accountPath string
	config      *edge_server.Config
	server      *server.Server
}

// StartServers starts configured nodes
func StartNodes(t testing.TB, bindAddr netip.Addr, genesisCfgPath, availAddr, accountPath string, nodeTypes ...consensus.MechanismType) (*Context, error) {
	t.Helper()

	ctx := &Context{}
	if err := createAvailAccounts(t, availAddr, nodeTypes); err != nil {
		t.Fatal(err)
	}

	// Set up a [TCP] port allocator.
	pa := NewPortAllocator(bindAddr)

	nnh := newNodeNameHelper()
	for _, nt := range nodeTypes {
		cfg, err := configureNode(t, pa, nt, genesisCfgPath)
		if err != nil {
			_ = pa.Release()
			return nil, err
		}

		si := instance{
			nodeType:    nt,
			config:      cfg.Config,
			accountPath: nnh.nextAccountPath(nt),
		}

		u, err := url.Parse(fmt.Sprintf("http://%s/", cfg.Config.JSONRPC.JSONRPCAddr.String()))
		if err != nil {
			return nil, err
		}
		ctx.jsonRPCURLs = append(ctx.jsonRPCURLs, u)

		ctx.servers = append(ctx.servers, si)
	}

	// Release allocated [TCP] ports to be used in Edge nodes.
	err := pa.Release()
	if err != nil {
		return nil, err
	}

	for i, si := range ctx.servers {
		bootnodes := make(map[consensus.MechanismType]string)

		// Adjust the blockchain Genesis spec.
		for j := range ctx.servers {
			if len(ctx.servers[j].config.Chain.Bootnodes) > 0 {
				// Collect one per node type. The logic here is that the
				// `bootstrap-sequencer` is the preferred one, but one `sequencer` is a
				// good second choice. If there are no sequencers -> return an error.
				//
				// In the chain spec, there is expected to be only one. See
				// `configureNode()` below.
				bootnodes[ctx.servers[j].nodeType] = ctx.servers[j].config.Chain.Bootnodes[0]
			}

			if i == j {
				// Skip `self` for the rest.
				continue
			}

			// Sync all premined accounts.
			for k, v := range ctx.servers[j].config.Chain.Genesis.Alloc {
				if _, exists := si.config.Chain.Genesis.Alloc[k]; !exists {
					si.config.Chain.Genesis.Alloc[k] = v
				}
			}
		}

		bootnodeAddr, exists := bootnodes[consensus.BootstrapSequencer]
		if !exists {
			bootnodeAddr, exists = bootnodes[consensus.Sequencer]
		}

		if !exists {
			return nil, fmt.Errorf("at least one sequencer must be configured")
		}

		// Reset the bootnode list.
		si.config.Chain.Bootnodes = []string{bootnodeAddr}
		si.config.Network.Chain.Bootnodes = []string{bootnodeAddr}

		srv, err := startNode(si.config, availAddr, si.accountPath, si.nodeType)
		if err != nil {
			return nil, err
		}

		ctx.servers[i].server = srv

		t.Logf("%d: started node %q", i, si.nodeType)
	}

	t.Logf("all %d nodes started", len(ctx.servers))

	return ctx, nil
}

func configureNode(t testing.TB, pa *PortAllocator, nodeType consensus.MechanismType, genesisCfgPath string) (*pkg_config.CustomServerConfig, error) {
	t.Helper()

	rawConfig := config.DefaultConfig()
	rawConfig.DataDir = t.TempDir()
	rawConfig.GenesisPath = genesisCfgPath

	chainSpec, err := chain.Import(genesisCfgPath)
	if err != nil {
		return nil, err
	}

	// Reset bootnodes, in case there're any in the JSON file.
	chainSpec.Bootnodes = nil

	jsonRpcAddr, err := pa.Allocate()
	if err != nil {
		return nil, err
	}

	grpcAddr, err := pa.Allocate()
	if err != nil {
		return nil, err
	}

	libp2pAddr, err := pa.Allocate()
	if err != nil {
		return nil, err
	}

	secretsManager, err := helper.SetupLocalSecretsManager(rawConfig.DataDir)
	if err != nil {
		return nil, err
	}

	_, err = helper.InitBLSValidatorKey(secretsManager)
	if err != nil {
		return nil, err
	}

	minerAddr, err := helper.InitECDSAValidatorKey(secretsManager)
	if err != nil {
		return nil, err
	}

	libp2pKey, err := helper.InitNetworkingPrivateKey(secretsManager)
	if err != nil {
		return nil, err
	}

	p2pID, err := peer.IDFromPrivateKey(libp2pKey)
	if err != nil {
		return nil, err
	}

	var bootnodeAddr string
	switch {
	case libp2pAddr.Addr().Is4():
		bootnodeAddr = fmt.Sprintf("/ip4/%s/tcp/%d/p2p/%s", libp2pAddr.Addr().String(), libp2pAddr.Port(), p2pID)
	case libp2pAddr.Addr().Is6():
		bootnodeAddr = fmt.Sprintf("/ip6/%s/tcp/%d/p2p/%s", libp2pAddr.Addr().String(), libp2pAddr.Port(), p2pID)
	default:
		return nil, fmt.Errorf("invalid p2p network address: %q", libp2pAddr.String())
	}

	if nodeType == consensus.BootstrapSequencer || len(chainSpec.Bootnodes) == 0 {
		chainSpec.Bootnodes = append(chainSpec.Bootnodes, bootnodeAddr)
	}

	chainSpec.Genesis.Alloc[minerAddr] = &chain.GenesisAccount{
		Balance: big.NewInt(0).Mul(big.NewInt(1000), common.ETH),
	}

	cfg := &pkg_config.CustomServerConfig{
		Config: &edge_server.Config{
			Chain: chainSpec,
			JSONRPC: &edge_server.JSONRPC{
				JSONRPCAddr:              net.TCPAddrFromAddrPort(jsonRpcAddr),
				AccessControlAllowOrigin: rawConfig.Headers.AccessControlAllowOrigins,
			},
			GRPCAddr:   net.TCPAddrFromAddrPort(grpcAddr),
			LibP2PAddr: net.TCPAddrFromAddrPort(libp2pAddr),
			Telemetry:  new(edge_server.Telemetry),
			Network: &network.Config{
				NoDiscover:       rawConfig.Network.NoDiscover,
				Addr:             net.TCPAddrFromAddrPort(libp2pAddr),
				DataDir:          rawConfig.DataDir,
				MaxPeers:         rawConfig.Network.MaxPeers,
				MaxInboundPeers:  rawConfig.Network.MaxInboundPeers,
				MaxOutboundPeers: rawConfig.Network.MaxOutboundPeers,
				Chain:            chainSpec,
			},
			DataDir:            rawConfig.DataDir,
			Seal:               true, // Seal enables TxPool P2P gossiping
			PriceLimit:         rawConfig.TxPool.PriceLimit,
			MaxAccountEnqueued: 128,
			MaxSlots:           rawConfig.TxPool.MaxSlots,
			SecretsManager:     nil,
			RestoreFile:        nil,
			LogLevel:           hclog.Info,
			LogFilePath:        rawConfig.LogFilePath,
		},
		NodeType: nodeType.String(),
	}

	return cfg, nil
}

func startNode(cfg *edge_server.Config, availAddr, accountPath string, nodeType consensus.MechanismType) (*server.Server, error) {
	var bootnode bool
	if nodeType == consensus.BootstrapSequencer {
		bootnode = true
	}

	availAccount, err := avail.AccountFromFile(accountPath)
	if err != nil {
		log.Fatalf("failed to read Avail account from %q: %s\n", accountPath, err)
	}

	availClient, err := avail.NewClient(availAddr)
	if err != nil {
		log.Fatalf("failed to create Avail client: %s\n", err)
	}

	appID, err := avail.EnsureApplicationKeyExists(availClient, avail.ApplicationKey, availAccount)
	if err != nil {
		log.Fatalf("failed to get AppID from Avail: %s\n", err)
	}

	availSender := avail.NewSender(availClient, appID, availAccount)

	consensusCfg := consensus.Config{
		Bootnode:          bootnode,
		AvailAccount:      availAccount,
		AvailClient:       availClient,
		AvailSender:       availSender,
		AccountFilePath:   accountPath,
		FraudListenerAddr: "",
		NodeType:          string(nodeType),
		AvailAppID:        appID,
	}

	serverInstance, err := server.NewServer(cfg, consensusCfg)
	if err != nil {
		return nil, fmt.Errorf("failure to start node: %w", err)
	}

	return serverInstance, nil
}

type PortAllocator struct {
	bindAddr  netip.Addr
	listeners []net.Listener
}

func NewPortAllocator(bindAddr netip.Addr) *PortAllocator {
	return &PortAllocator{
		bindAddr: bindAddr,
	}
}

func (pa *PortAllocator) Allocate() (netip.AddrPort, error) {
	addrPort := netip.AddrPortFrom(pa.bindAddr, 0)
	lst, err := net.Listen("tcp", addrPort.String())
	if err != nil {
		return netip.AddrPort{}, err
	}

	pa.listeners = append(pa.listeners, lst)

	return netip.ParseAddrPort(lst.Addr().String())
}

func (pa *PortAllocator) Release() error {
	var lastErr error

	for _, l := range pa.listeners {
		err := l.Close()
		if err != nil {
			lastErr = err
			log.Printf("error: %#v", err)
		}
	}

	return lastErr
}

func (sc *Context) GethClient() (*ethclient.Client, error) {
	if len(sc.jsonRPCURLs) == 0 {
		return nil, fmt.Errorf("no json-rpc URLs available")
	}

	return ethclient.Dial(sc.jsonRPCURLs[0].String())
}

func (sc *Context) JSONRPCURLs() []*url.URL {
	return sc.jsonRPCURLs
}

func (sc *Context) StopAll() {
	for _, srvInstance := range sc.servers {
		srvInstance.server.Close()
	}
}

// FirstRPCURLForNodeType looks up and returns the url of the node for the node type
func (sc *Context) FirstRPCURLForNodeType(nodeType consensus.MechanismType) (*url.URL, error) {
	if len(sc.servers) != len(sc.jsonRPCURLs) {
		return nil, fmt.Errorf("servers and jsonRPCURLs have different lengths")
	}
	for i, srv := range sc.servers {
		if srv.nodeType == nodeType {
			return sc.jsonRPCURLs[i], nil
		}
	}

	return nil, fmt.Errorf("no %s node present in the servers", nodeType)
}

func createAvailAccounts(t testing.TB, availAddr string, nodeTypes []consensus.MechanismType) error {
	t.Helper()

	nnh := newNodeNameHelper()

	var accountWg sync.WaitGroup

	availClient, err := avail.NewClient(availAddr)
	if err != nil {
		return err
	}

	var nonceIncrement uint64
	for _, nt := range nodeTypes {
		accountWg.Add(1)

		go func(accountPath string, nonceIncrement uint64) {
			defer accountWg.Done()
			// Initiate creation of the avail account if not present
			err := createAvailAccount(t, availClient, accountPath, nonceIncrement)
			if err != nil {
				t.Fatalf("failed to create new avail account: %s", err)
				return
			}
		}(nnh.nextAccountPath(nt), nonceIncrement)

		nonceIncrement++
		time.Sleep(250 * time.Millisecond)
	}

	t.Log("Waiting for Avail accounts to be created...")
	accountWg.Wait()
	t.Log("Avail accounts created")
	return err
}

func createAvailAccount(t testing.TB, availClient avail.Client, accountPath string, nonceIncrement uint64) error {
	t.Helper()

	// If file exists, make sure that we return the file and not go through account creation process.
	// In rare cases, funds may be depleted but in that case we can erase files and run it again.
	// TODO: Potentially add lookup for account balance check and if it's too low, process with creation
	if _, err := os.Stat(accountPath); !errors.Is(err, os.ErrNotExist) {
		// In case that account path exists but is not visible in Avail (restart)
		// make sure to go through the process of the account creation.
		if ok, err := avail.AccountExistsFromMnemonic(availClient, accountPath); err == nil && ok {
			return nil
		}
	}

	availAccount, err := avail.NewAccount()
	if err != nil {
		return err
	}

	err = avail.DepositBalance(availClient, availAccount, 15*avail.AVL, nonceIncrement)
	if err != nil {
		return err
	}

	if _, err := avail.QueryAppID(availClient, avail.ApplicationKey); err != nil {
		if err == avail.ErrAppIDNotFound {
			_, err = avail.EnsureApplicationKeyExists(availClient, avail.ApplicationKey, availAccount)
			if err != nil {
				return err
			}
		}

		return err
	}

	t.Logf("Successfully deposited '%d' AVL to '%s'", 15, availAccount.Address)

	if err := os.WriteFile(accountPath, []byte(availAccount.URI), 0o644); err != nil {
		return err
	}

	t.Logf("Successfully written mnemonic into '%s'", accountPath)

	return nil
}

type nodeNameHelper map[consensus.MechanismType]int

func newNodeNameHelper() nodeNameHelper { return make(nodeNameHelper) }

func (h nodeNameHelper) next(nodeType consensus.MechanismType) string {
	h[nodeType]++
	return fmt.Sprintf("%s-%d", nodeType, h[nodeType])
}

func (h nodeNameHelper) nextAccountPath(nodeType consensus.MechanismType) string {
	return path.Join(testAccountPath, h.next(nodeType))
}
