package renter

import (
	"context"
	"fmt"
	"github.com/TRON-US/go-btfs/core/commands/storage/ds"
	"github.com/ipfs/go-datastore"
	"github.com/looplab/fsm"
	shardpb "github.com/tron-us/go-btfs-common/protos/btfs/shard"
	"github.com/tron-us/protobuf/proto"
	"time"
)

const (
	shard_metadata_key = "/peers/%s/v0.0.1/renter/sessions/%s/shards/%s/metadata"
	shard_status_key   = "/peers/%s/v0.0.1/renter/sessions/%s/shards/%s/status"
)

type Shard struct {
	ctx       context.Context
	step      chan interface{}
	fsm       *fsm.FSM
	peerId    string
	sessionId string
	shardHash string
	ds        datastore.Datastore
}

func NewShard(ctx context.Context, ds datastore.Datastore, peerId string, sessionId string, shardHash string) *Shard {
	s := &Shard{
		ctx:       ctx,
		ds:        ds,
		peerId:    peerId,
		sessionId: sessionId,
		shardHash: shardHash,
		step:      make(chan interface{}),
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
	fmt.Println("enter state:", e.Dst)
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
	switch e.Dst {
	case "init":
		s.init(e.Args[0].(*shardpb.Metadata))
	case "error":
		s.error(e.Args[0].(error))
	}
}

func (s *Shard) ToInit(md *shardpb.Metadata) error {
	return s.fsm.Event("toInit", md)
}

func (s *Shard) ToContract() error {
	s.step <- struct{}{}
	return s.fsm.Event("toContract")
}

func (s *Shard) ToComplete() error {
	s.step <- struct{}{}
	return s.fsm.Event("toComplete")
}

func (s *Shard) ToError(err error) error {
	s.step <- struct{}{}
	return s.fsm.Event("toError", err)
}

func (s *Shard) Timeout() error {
	return s.fsm.Event("toError")
}

func (s *Shard) init(md *shardpb.Metadata) error {
	status := &shardpb.Status{
		Status:  "init",
		Message: "",
	}
	ks := []string{
		fmt.Sprintf(shard_status_key, s.peerId, s.sessionId, s.shardHash),
		fmt.Sprintf(shard_metadata_key, s.peerId, s.sessionId, s.shardHash),
	}
	vs := []proto.Message{
		status, md,
	}
	return ds.Batch(s.ds, ks, vs)
}

func (s *Shard) error(err error) error {
	status := &shardpb.Status{
		Status:  "error",
		Message: err.Error(),
	}
	return ds.Save(s.ds, fmt.Sprintf(shard_status_key, s.peerId, s.sessionId, s.shardHash), status)
}

func (s *Shard) Status() (*shardpb.Status, error) {
	md := &shardpb.Status{}
	err := ds.Get(s.ds, fmt.Sprintf(shard_status_key, s.peerId, s.sessionId, s.shardHash), md)
	return md, err
}
