package renter

import (
	"context"
	"github.com/google/uuid"
	"github.com/looplab/fsm"
	"time"
)

type Shard struct {
	Index                    int
	SessionId                string
	Time                     time.Time
	FileHash                 string
	ShardHash                string
	ShardFileSize            int64
	StorageLength            int64
	Status                   string
	ContractId               string
	Receiver                 string
	Price                    int64
	TotalPay                 int64
	HalfSignedEscrowContract []byte
	HalfSignedGuardContract  []byte
	StartTime                time.Time
	ContractLength           time.Duration
	ctx                      context.Context
	step                     chan interface{}
	fsm                      *fsm.FSM
}

func NewShard(ctx context.Context, index int, sessionId string, fileHash string, shardHash string,
	shardFileSize int64, storageLength int64, receiver string) *Shard {
	s := &Shard{
		SessionId:     sessionId,
		Index:         index,
		Time:          time.Now().UTC(),
		FileHash:      fileHash,
		ShardHash:     shardHash,
		ShardFileSize: shardFileSize,
		StorageLength: storageLength,
		ContractId:    uuid.New().String(),
		Receiver:      receiver,
		StartTime:     time.Now().UTC(),
		ctx:           ctx,
		step:          make(chan interface{}),
	}
	s.fsm = fsm.NewFSM("new",
		fsm.Events{
			{Name: "toInit", Src: []string{"new"}, Dst: "init"},
			{Name: "toContract", Src: []string{"init"}, Dst: "Contract"},
			{Name: "toComplete", Src: []string{"contract"}, Dst: "Complete"},
			{Name: "toError", Src: []string{"init", "contract", "complete"}, Dst: "Error"},
		},
		fsm.Callbacks{
			"enter_state": s.enterState,
		})
	return s
}

func (s *Shard) enterState(e *fsm.Event) {
	s.Status = e.Dst
	t, ok := timeouts[e.Dst]
	if ok {
		go func() {
			tick := time.Tick(t)
			select {
			case <-s.step:
				break
			case <-tick:
				s.Timeout()
				break
			}
		}()
	}
}

func (s *Shard) ToInit() error {
	return s.fsm.Event("toInit")
}

func (s *Shard) ToContract() error {
	s.step <- struct{}{}
	return s.fsm.Event("toContract")
}

func (s *Shard) ToComplete() error {
	s.step <- struct{}{}
	return s.fsm.Event("toComplete")
}

func (s *Shard) Timeout() error {
	return s.fsm.Event("toError")
}
