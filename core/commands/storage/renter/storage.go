package renter

import (
	"context"
	"fmt"
	cmds "github.com/TRON-US/go-btfs-cmds"
	"github.com/TRON-US/go-btfs/core/commands/cmdenv"
	"github.com/TRON-US/go-btfs/core/commands/storage"
	"github.com/TRON-US/go-btfs/core/corehttp/remote"
	"github.com/TRON-US/go-btfs/core/escrow"
	"github.com/TRON-US/go-btfs/core/hub"
	coreiface "github.com/TRON-US/interface-go-btfs-core"
	"github.com/TRON-US/interface-go-btfs-core/path"
	"github.com/google/uuid"
	cidlib "github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"
	shardpb "github.com/tron-us/go-btfs-common/protos/btfs/shard"
	"math"
	"strconv"
	"time"
)

const (
	uploadPriceOptionName   = "price"
	storageLengthOptionName = "storage-length"
	defaultStorageLength    = 30
)

// TODO: get/set the value from/in go-btfs-common
var HostPriceLowBoundary = int64(10)

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
	},
	Options: []cmds.Option{
		cmds.Int64Option(uploadPriceOptionName, "p", "Max price per GiB per day of storage in BTT."),
		cmds.IntOption(storageLengthOptionName, "len", "File storage period on hosts in days.").WithDefault(defaultStorageLength),
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

		hp := GetHostProvider(req.Context, n, cfg.Experimental.HostsSyncMode, api)

		price, found := req.Options[uploadPriceOptionName].(int64)
		if found && price < HostPriceLowBoundary {
			return fmt.Errorf("price is smaller than minimum setting price")
		}
		if found && price >= math.MaxInt64 {
			return fmt.Errorf("price should be smaller than max int64")
		}
		ns, err := hub.GetSettings(req.Context, cfg.Services.HubDomain,
			n.Identity.String(), n.Repo.Datastore())
		if err != nil {
			return err
		}
		if !found {
			price = int64(ns.StoragePriceAsk)
		}

		// init
		session := NewSession(req.Context, n.Repo.Datastore(), n.Identity.String())
		session.ToInit(n.Identity.String(), hashStr)
		shardHashes := make([]string, 0)
		shardSize, err := getContractSizeFromCid(req.Context, hashes[0], api)
		if err != nil {
			return err
		}
		storageLength := req.Options[storageLengthOptionName].(int)
		if uint64(storageLength) < ns.StorageTimeMin {
			return fmt.Errorf("invalid storage len. want: >= %d, got: %d",
				ns.StorageTimeMin, storageLength)
		}
		for i, h := range hashes {
			shardHashes = append(shardHashes, h.String())
			host, err := hp.NextValidHost()
			if err != nil {
				return err
			}
			peerId, err := peer.IDB58Decode(host)
			if err != nil {
				return err
			}
			contract, err := escrow.NewContract(cfg, uuid.New().String(), n, peerId, price, false, 0)
			if err != nil {
				return fmt.Errorf("create escrow contract failed: [%v] ", err)
			}
			halfSignedEscrowContract, err := escrow.SignContractAndMarshal(contract, nil, n.PrivateKey, true)
			if err != nil {
				return fmt.Errorf("sign escrow contract and maorshal failed: [%v] ", err)
			}

			fmt.Println(1)
			metadata, err := session.GetMetadata()
			if err != nil {
				return err
			}
			fmt.Println(2)
			s := NewShard(req.Context, n.Repo.Datastore(), n.Identity.String(), session.Id, h.String())

			fmt.Println(3)
			md := &shardpb.Metadata{
				Index:          int32(i),
				SessionId:      session.Id,
				FileHash:       metadata.FileHash,
				ShardFileSize:  int64(shardSize),
				StorageLength:  int64(storageLength),
				ContractId:     session.Id,
				Receiver:       host,
				Price:          price,
				TotalPay:       0,
				StartTime:      time.Now().UTC(),
				ContractLength: time.Duration(storageLength*24) * time.Hour,
			}
			guardContractMeta, err := NewContract2(md, h.String(), cfg, peerId.String())
			if err != nil {
				return fmt.Errorf("fail to new contract meta: [%v] ", err)
			}
			halfSignGuardContract, err := SignedContractAndMarshal(guardContractMeta, nil, n.PrivateKey, true,
				false, n.Identity.Pretty(), n.Identity.Pretty())
			if err != nil {
				return fmt.Errorf("fail to sign guard contract and marshal: [%v] ", err)
			}

			fmt.Println(4)
			md.HalfSignedEscrowContract = halfSignedEscrowContract
			md.HalfSignedGuardContract = halfSignGuardContract
			s.ToInit(md)

			fmt.Println(5)
			fmt.Println("session.Id", session.Id)
			fmt.Println("metadata.FileHash", metadata.FileHash)
			fmt.Println("h.String()", h.String())
			fmt.Println("md.Price", strconv.FormatInt(md.Price, 10))
			fmt.Println("halfSignedEscrowContract", len(halfSignedEscrowContract))
			fmt.Println("halfSignGuardContract", len(halfSignGuardContract))
			fmt.Println("md.StorageLength", md.StorageLength)
			fmt.Println("md.ShardFileSize", md.ShardFileSize)
			fmt.Println("i", i)
			_, err = remote.P2PCall(req.Context, n, peerId, "/storage/upload/init",
				session.Id,
				metadata.FileHash,
				h.String(),
				strconv.FormatInt(md.Price, 10),
				halfSignedEscrowContract,
				halfSignGuardContract,
				strconv.FormatInt(md.StorageLength, 10),
				strconv.FormatInt(md.ShardFileSize, 10),
				strconv.Itoa(i),
			)
			if err != nil {
				fmt.Println("p2p call err", err)
				s.ToError(err)
			} else {
				s.ToContract()
			}
			status, err := s.Status()
			if err != nil {
				fmt.Println("error when get status:", err.Error())
			}
			fmt.Println("hash", h.String(), "status", status.Status, "msg", status.Message)
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
