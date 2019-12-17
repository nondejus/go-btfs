package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/TRON-US/go-btfs/core"

	coreiface "github.com/TRON-US/interface-go-btfs-core"
	"github.com/alecthomas/units"
	"github.com/google/uuid"
	cidlib "github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p-core/peer"
	guardPb "github.com/tron-us/go-btfs-common/protos/guard"
)

var GlobalSession *SessionMap
var StdStateFlow [7]*FlowControl
var StdSessionStateFlow [4]*FlowControl

const (
	FileContractsStorePrefix = "/file-contracts/"
	ShardsStorePrefix        = "/shards/"

	// chunk state
	InitState      = 0
	UploadState    = 1
	ChallengeState = 2
	SolveState     = 3
	VerifyState    = 4
	PaymentState   = 5
	CompleteState  = 6

	// session status
	InitStatus     = 0
	UploadStatus   = 1
	CompleteStatus = 2
	ErrStatus      = 3
)

type FlowControl struct {
	State   string
	TimeOut time.Duration
}

type SessionMap struct {
	sync.Mutex
	Map map[string]*FileContracts
}

type FileContracts struct {
	sync.Mutex

	Time              time.Time
	GuardContracts    []*guardPb.Contract
	Renter            peer.ID
	FileHash          cidlib.Cid
	Status            string
	ShardInfo         map[string]*Shards // mapping chunkHash with Shards info
	CompleteChunks    int
	CompleteContracts int
	RetryQueue        *RetryQueue

	SessionStatusChan chan StatusChan
}

type StatusChan struct {
	CurrentStep int
	Succeed     bool
	Err         error
}

type Shards struct {
	sync.Mutex

	ContractID           string
	Challenge            *StorageChallenge
	SignedEscrowContract []byte
	Receiver             peer.ID
	Price                int64
	TotalPay             int64
	State                int
	ShardSize            int64
	StorageLength        int64
	ContractLength       time.Duration
	StartTime            time.Time
	Err                  error

	RetryChan chan *StepRetryChan
}

type StepRetryChan struct {
	CurrentStep       int
	Succeed           bool
	ClientErr         error
	HostErr           error
	SessionTimeOutErr error
}

func init() {
	GlobalSession = &SessionMap{}
	GlobalSession.Map = make(map[string]*FileContracts)
	// init chunk state
	StdStateFlow[InitState] = &FlowControl{
		State:   "init",
		TimeOut: 10 * time.Second}
	StdStateFlow[UploadState] = &FlowControl{
		State:   "upload",
		TimeOut: 10 * time.Second}
	StdStateFlow[ChallengeState] = &FlowControl{
		State:   "challenge",
		TimeOut: 10 * time.Second}
	StdStateFlow[SolveState] = &FlowControl{
		State:   "solve",
		TimeOut: 30 * time.Second}
	StdStateFlow[VerifyState] = &FlowControl{
		State:   "verify",
		TimeOut: time.Second}
	StdStateFlow[PaymentState] = &FlowControl{
		State:   "payment",
		TimeOut: 10 * time.Second}
	StdStateFlow[CompleteState] = &FlowControl{
		State:   "complete",
		TimeOut: 5 * time.Second}
	// init session status
	StdSessionStateFlow[InitStatus] = &FlowControl{
		State:   "init",
		TimeOut: time.Minute}
	StdSessionStateFlow[UploadStatus] = &FlowControl{
		State:   "upload",
		TimeOut: 5 * time.Minute}
	StdSessionStateFlow[CompleteStatus] = &FlowControl{
		State: "complete"}
	StdSessionStateFlow[ErrStatus] = &FlowControl{
		State: "error",
	}
}

func (sm *SessionMap) PutSession(ssID string, ss *FileContracts) {
	sm.Lock()
	defer sm.Unlock()

	if ss == nil {
		ss = &FileContracts{}
	}
	sm.Map[ssID] = ss
}

func (sm *SessionMap) GetSession(ssID string) (*FileContracts, error) {
	sm.Lock()
	defer sm.Unlock()

	if sm.Map[ssID] == nil {
		return nil, fmt.Errorf("session id doesn't exist")
	}
	return sm.Map[ssID], nil
}

func (sm *SessionMap) GetOrDefault(ssID string, pid peer.ID) *FileContracts {
	sm.Lock()
	defer sm.Unlock()

	if sm.Map[ssID] == nil {
		ss := &FileContracts{}
		ss.new(pid)
		sm.Map[ssID] = ss
		return ss
	}
	return sm.Map[ssID]
}

func (sm *SessionMap) Remove(ssID string, chunkHash string) {
	sm.Lock()
	defer sm.Unlock()

	if ss := sm.Map[ssID]; ss != nil {
		if chunkHash != "" {
			ss.RemoveShard(chunkHash)
		}
		if len(ss.ShardInfo) == 0 {
			delete(sm.Map, ssID)
		}
	}
}

func NewSessionID() (string, error) {
	ssid, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return ssid.String(), nil
}

func PersistFileMetaToDatabase(node *core.IpfsNode, ssID string) error {
	rds := node.Repo.Datastore()
	ss, err := GlobalSession.GetSession(ssID)
	if err != nil {
		return err
	}
	fileContractsBytes, err := json.Marshal(ss)
	if err != nil {
		return err
	}
	err = rds.Put(ds.NewKey(FileContractsStorePrefix+ssID), fileContractsBytes)
	if err != nil {
		return err
	}
	for chunkHash, chunkInfo := range ss.ShardInfo {
		shardBytes, err := json.Marshal(chunkInfo)
		if err != nil {
			return err
		}
		err = rds.Put(ds.NewKey(ShardsStorePrefix+chunkHash), shardBytes)
		if err != nil {
			return err
		}
	}
	return nil
}

func (ss *FileContracts) new(pid peer.ID) {
	ss.Lock()
	defer ss.Unlock()

	ss.Renter = pid
	ss.Time = time.Now()
	ss.Status = "init"
	ss.ShardInfo = make(map[string]*Shards)
	ss.SessionStatusChan = make(chan StatusChan)
}

func (ss *FileContracts) CompareAndSwap(desiredStatus int, targetStatus int) bool {
	ss.Lock()
	defer ss.Unlock()

	// if current status isn't expected status,
	// can't setting new status
	if StdSessionStateFlow[desiredStatus].State != ss.Status {
		return false
	} else {
		ss.Status = StdSessionStateFlow[targetStatus].State
		return true
	}
}

func (ss *FileContracts) SetRetryQueue(q *RetryQueue) {
	ss.Lock()
	defer ss.Unlock()

	ss.RetryQueue = q
}

func (ss *FileContracts) GetRetryQueue() *RetryQueue {
	ss.Lock()
	defer ss.Unlock()

	return ss.RetryQueue
}

func (ss *FileContracts) UpdateCompleteShardNum(diff int) {
	ss.Lock()
	defer ss.Unlock()

	ss.CompleteChunks += diff
}

func (ss *FileContracts) GetCompleteShards() int {
	ss.Lock()
	defer ss.Unlock()

	return ss.CompleteChunks
}

func (ss *FileContracts) SetFileHash(fileHash cidlib.Cid) {

	ss.Lock()
	defer ss.Unlock()

	ss.FileHash = fileHash
}

func (ss *FileContracts) GetFileHash() cidlib.Cid {

	ss.Lock()
	defer ss.Unlock()

	return ss.FileHash
}

func (ss *FileContracts) IncrementContract(chunkHash string, contracts []byte, guardContract *guardPb.Contract) error {
	ss.Lock()
	defer ss.Unlock()

	ss.GuardContracts = append(ss.GuardContracts, guardContract)
	chunk := ss.ShardInfo[chunkHash]
	if chunk == nil {
		return fmt.Errorf("chunk does not exists")
	}
	chunk.SetSignedContract(contracts)
	ss.CompleteContracts++
	return nil
}

func (ss *FileContracts) GetGuardContracts() []*guardPb.Contract {
	ss.Lock()
	defer ss.Unlock()

	return ss.GuardContracts
}

func (ss *FileContracts) GetCompleteContractNum() int {
	ss.Lock()
	defer ss.Unlock()

	return ss.CompleteContracts
}

func (ss *FileContracts) SetStatus(status int) {
	ss.Lock()
	defer ss.Unlock()

	ss.Status = StdSessionStateFlow[status].State
}
func (ss *FileContracts) GetStatus() string {
	ss.Lock()
	defer ss.Unlock()

	return ss.Status
}

func (ss *FileContracts) GetShard(hash string) (*Shards, error) {
	ss.Lock()
	defer ss.Unlock()

	if ss.ShardInfo[hash] == nil {
		return nil, fmt.Errorf("chunk hash doesn't exist ")
	}
	return ss.ShardInfo[hash], nil
}

func (ss *FileContracts) RemoveShard(hash string) {
	ss.Lock()
	defer ss.Unlock()

	if ss.ShardInfo[hash] != nil {
		delete(ss.ShardInfo, hash)
	}
}

func (ss *FileContracts) GetOrDefault(hash string, shardSize int64, length int64, price int64) *Shards {
	ss.Lock()
	defer ss.Unlock()

	if ss.ShardInfo[hash] == nil {
		c := &Shards{}
		c.RetryChan = make(chan *StepRetryChan)
		c.StartTime = time.Now()
		c.State = InitState
		c.ShardSize = shardSize
		c.StorageLength = length
		c.ContractLength = time.Duration(length*24) * time.Hour
		c.Price = price
		c.TotalPay = int64(float64(shardSize) / float64(units.GiB) * float64(price) * float64(length))
		ss.ShardInfo[hash] = c
		return c
	}
	return ss.ShardInfo[hash]
}

func (c *Shards) SetContractID(ssID string, shardHash string) {
	c.Lock()
	defer c.Unlock()

	c.ContractID = ssID + shardHash
}

func (c *Shards) GetContractID() string {
	c.Lock()
	defer c.Unlock()

	return c.ContractID
}

func (c *Shards) UpdateShard(recvPid peer.ID) {
	c.Lock()
	defer c.Unlock()

	c.Receiver = recvPid
	c.StartTime = time.Now()
}

func (c *Shards) SetSignedContract(contract []byte) {
	c.Lock()
	defer c.Unlock()

	c.SignedEscrowContract = contract
}

// used on client to record a new challenge
func (c *Shards) SetChallenge(ctx context.Context, n *core.IpfsNode, api coreiface.CoreAPI,
	rootCid, shardCid cidlib.Cid) (*StorageChallenge, error) {
	c.Lock()
	defer c.Unlock()

	var sch *StorageChallenge
	var err error
	// if the chunk hasn't been generated challenge before
	if c.Challenge == nil {
		sch, err = NewStorageChallenge(ctx, n, api, rootCid, shardCid)
		if err != nil {
			return nil, err
		}
		c.Challenge = sch
	} else {
		sch = c.Challenge
	}

	if err = sch.GenChallenge(); err != nil {
		return nil, err
	}
	c.StartTime = time.Now()
	return sch, nil
}

// usually used on host, to record host challenge info
func (c *Shards) UpdateChallenge(sch *StorageChallenge) {
	c.Lock()
	defer c.Unlock()

	c.Challenge = sch
	c.StartTime = time.Now()
}

func (c *Shards) SetState(state int) {
	c.Lock()
	defer c.Unlock()

	c.State = state
	c.StartTime = time.Now()
}

func (c *Shards) GetState() string {
	c.Lock()
	defer c.Unlock()

	return StdStateFlow[c.State].State
}

func (c *Shards) GetTotalAmount() int64 {
	c.Lock()
	defer c.Unlock()

	return c.TotalPay
}

func (c *Shards) SetTime(time time.Time) {
	c.Lock()
	defer c.Unlock()

	c.StartTime = time
}

func (c *Shards) GetTime() time.Time {
	c.Lock()
	defer c.Unlock()

	return c.StartTime
}