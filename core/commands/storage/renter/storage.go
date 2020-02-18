package renter

import (
	"context"
	"fmt"
	cmds "github.com/TRON-US/go-btfs-cmds"
	"github.com/TRON-US/go-btfs/core/commands/cmdenv"
	"github.com/TRON-US/go-btfs/core/commands/storage"
	"github.com/TRON-US/go-btfs/core/escrow"
	coreiface "github.com/TRON-US/interface-go-btfs-core"
	"github.com/TRON-US/interface-go-btfs-core/path"
	"github.com/google/uuid"
	cidlib "github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"
	"time"
)

const (
	leafHashOptionName               = "leaf-hash"
	uploadPriceOptionName            = "price"
	replicationFactorOptionName      = "replication-factor"
	hostSelectModeOptionName         = "host-select-mode"
	hostSelectionOptionName          = "host-selection"
	testOnlyOptionName               = "host-search-local"
	storageLengthOptionName          = "storage-length"
	repairModeOptionName             = "repair-mode"
	customizedPayoutOptionName       = "customize-payout"
	customizedPayoutPeriodOptionName = "customize-payout-period"

	defaultRepFactor     = 3
	defaultStorageLength = 30
)

var StorageCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Interact with storage services on BTFS.",
		ShortDescription: `
Storage services include client upload operations, host storage operations,
host information sync/display operations, and BTT payment-related routines.`,
	},
	Subcommands: map[string]*cmds.Command{
		"upload": StorageUploadCmd,
	},
}

type UploadRes struct {
	ID string
}

var StorageUploadCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Store files on BTFS network nodes through BTT payment.",
		ShortDescription: `
By default, BTFS selects hosts based on overall score according to the current client's environment.
To upload a file, <file-hash> must refer to a reed-solomon encoded file.

To create a reed-solomon encoded file from a normal file:

    $ btfs add --chunker=reed-solomon <file>
    added <file-hash> <file>

Run command to upload:

    $ btfs storage upload <file-hash>

To custom upload and store a file on specific hosts:
    Use -m with 'custom' mode, and put host identifiers in -s, with multiple hosts separated by ','.

    # Upload a file to a set of hosts
    # Total # of hosts (N) must match # of shards in the first DAG level of root file hash
    $ btfs storage upload <file-hash> -m=custom -s=<host1-peer-id>,<host2-peer-id>,...,<hostN-peer-id>

    # Upload specific shards to a set of hosts
    # Total # of hosts (N) must match # of shards given
	$ btfs storage upload <shard-hash1> <shard-hash2> ... <shard-hashN> -l -m=custom -s=<host1-peer-id>,<host2-peer-id>,...,<hostN-peer-id>

Use status command to check for completion:
    $ btfs storage upload status <session-id> | jq`,
	},
	Subcommands: map[string]*cmds.Command{},
	Arguments: []cmds.Argument{
		cmds.StringArg("file-hash", true, false, "Hash of file to upload."),
		cmds.StringArg("repair-shards", false, false, "Shard hashes to repair."),
		cmds.StringArg("renter-pid", false, false, "Original renter peer ID."),
		cmds.StringArg("blacklist", false, false, "Blacklist of hosts during upload."),
	},
	Options: []cmds.Option{
		cmds.BoolOption(leafHashOptionName, "l", "Flag to specify given hash(es) is leaf hash(es).").WithDefault(false),
		cmds.Int64Option(uploadPriceOptionName, "p", "Max price per GiB per day of storage in BTT."),
		cmds.IntOption(replicationFactorOptionName, "r", "Replication factor for the file with erasure coding built-in.").WithDefault(defaultRepFactor),
		cmds.StringOption(hostSelectModeOptionName, "m", "Based on this mode to select hosts and upload automatically. Default: mode set in config option Experimental.HostsSyncMode."),
		cmds.StringOption(hostSelectionOptionName, "s", "Use only these selected hosts in order on 'custom' mode. Use ',' as delimiter."),
		cmds.BoolOption(testOnlyOptionName, "t", "Enable host search under all domains 0.0.0.0 (useful for local test)."),
		cmds.IntOption(storageLengthOptionName, "len", "File storage period on hosts in days.").WithDefault(defaultStorageLength),
		cmds.BoolOption(repairModeOptionName, "rm", "Enable repair mode.").WithDefault(false),
		cmds.BoolOption(customizedPayoutOptionName, "Enable file storage customized payout schedule.").WithDefault(false),
		cmds.IntOption(customizedPayoutPeriodOptionName, "Period of customized payout schedule.").WithDefault(1),
	},
	RunTimeout: 15 * time.Minute,
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		// get hosts
		cfg, err := cmdenv.GetConfig(env)
		if err != nil {
			return err
		}

		// get node
		n, err := cmdenv.GetNode(env)
		if err != nil {
			return err
		}
		mode := cfg.Experimental.HostsSyncMode

		// init
		session := NewFileSession(req.Context)
		session.Filehash = req.Arguments[0]
		session.RenterPid = n.Identity
		session.ToInit()

		// get core api
		api, err := cmdenv.GetApi(env, req)
		if err != nil {
			return err
		}

		// get shardes
		if len(req.Arguments) != 1 {
			return fmt.Errorf("need one and only one root file hash")
		}
		hashStr := req.Arguments[0]
		rootHash, err := cidlib.Parse(hashStr)
		if err != nil {
			return err
		}
		hashes, err := storage.CheckAndGetReedSolomonShardHashes(req.Context, n, api, rootHash)
		if err != nil || len(hashes) == 0 {
			return fmt.Errorf("invalid hash: %s", err)
		}

		hosts, err := storage.GetHostsFromDatastore(req.Context, n, mode, len(hashes))
		for _, h := range hosts {
			fmt.Println("host", h.NodeId, h.StoragePriceAsk)
		}
		if err != nil {
			return err
		}

		shardHashes := make([]string, 0)
		idx := 0
		for i, h := range hashes {
			shardHashes = append(shardHashes, h.String())
			fmt.Println("hash i", i, h.String())

			peerId, _ := peer.IDB58Decode(hosts[idx].String())
			contract, _ := escrow.NewContract(cfg, uuid.New().String(), n, peerId, 0, false, 0)
			if err != nil {
				return fmt.Errorf("create escrow contract failed: [%v] ", err)
			}
			halfSignedEscrowContract, err := escrow.SignContractAndMarshal(contract, nil, n.PrivateKey, true)
			if err != nil {
				return fmt.Errorf("sign escrow contract and maorshal failed: [%v] ", err)
			}
			fmt.Println(halfSignedEscrowContract)
			idx++
		}

		seRes := &UploadRes{
			ID: session.Id,
		}
		return res.Emit(seRes)
	},
	Type: UploadRes{},
}

func getContractSizeFromCid(ctx context.Context, hash cidlib.Cid, api coreiface.CoreAPI) (uint64, error) {
	leafPath := path.IpfsPath(hash)
	ipldNode, err := api.ResolveNode(ctx, leafPath)
	if err != nil {
		return 0, err
	}
	return ipldNode.Size()
}
