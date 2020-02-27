package renter

import (
	"context"
	"errors"
	"fmt"
	"github.com/TRON-US/go-btfs/core/commands/storage/ds"
	"github.com/google/uuid"
	"github.com/ipfs/go-datastore"
	"github.com/looplab/fsm"
	renterpb "github.com/tron-us/go-btfs-common/protos/btfs/renter"
	"github.com/tron-us/protobuf/proto"
	"time"
)

var (
	fsmEvents = fsm.Events{
		{Name: "toInit", Src: []string{""}, Dst: "init"},
		{Name: "toSubmit", Src: []string{"init"}, Dst: "submit"},
		{Name: "toPay", Src: []string{"submit"}, Dst: "pay"},
		{Name: "toGuard", Src: []string{"pay"}, Dst: "guard"},
		{Name: "toComplete", Src: []string{"guard"}, Dst: "complete"},
		{Name: "toError", Src: []string{"init", "submit", "pay", "guard"}, Dst: "error"},
	}
)

const (
	session_metadata_key  = "/btfs/%s/v0.0.1/renter/sessions/%s/metadata"
	session_status_key    = "/btfs/%s/v0.0.1/renter/sessions/%s/status"
	session_contracts_key = "/btfs/%s/v0.0.1/renter/sessions/%s/contracts"
)

type Session struct {
	Id     string
	peerId string
	ctx    context.Context
	fsm    *fsm.FSM
	ds     datastore.Datastore
}

func GetSession(ctx context.Context, ds datastore.Datastore, peerId string, sessionId string) (*Session, error) {
	f := &Session{
		Id:     sessionId,
		ctx:    ctx,
		ds:     ds,
		peerId: peerId,
	}
	status, err := f.GetStatus()
	if err != nil {
		return nil, err
	}
	f.fsm = fsm.NewFSM(status.Status, fsmEvents, fsm.Callbacks{
		"enter_state": f.enterState,
	})
	return f, nil
}

func NewSession(ctx context.Context, ds datastore.Datastore, peerId string) *Session {
	f := &Session{
		Id:     uuid.New().String(),
		ctx:    ctx,
		ds:     ds,
		peerId: peerId,
	}
	f.fsm = fsm.NewFSM("", fsmEvents, fsm.Callbacks{
		"enter_state": f.enterState,
	})
	return f
}

func (f *Session) enterState(e *fsm.Event) {
	fmt.Println("session enter state:", e.Dst)
	switch e.Dst {
	case "init":
		err := f.init(e.Args[0].(string), e.Args[1].(string), e.Args[2].([]string))
		if err != nil {
			log.Error("err", err)
		}
	case "error":
		f.error(e.Args[0].(error))
	}
}

func (f *Session) ToInit(renterId string, fileHash string, hashes []string) error {
	return f.fsm.Event("toInit", renterId, fileHash, hashes)
}

func (f *Session) ToSubmit() error {
	return f.fsm.Event("toSubmit")
}

func (f *Session) ToPay() error {
	return f.fsm.Event("toPay")
}

func (f *Session) ToGuard() error {
	return f.fsm.Event("toGuard")
}

func (f *Session) ToComplete() error {
	return f.fsm.Event("toComplete")
}

func (f *Session) ToError(err error) error {
	return f.fsm.Event("toError", err)
}

func (f *Session) Timeout() error {
	return f.fsm.Event("toError", errors.New("timeout"))
}

func (f *Session) init(renterId string, fileHash string, hashes []string) error {
	status := &renterpb.Status{
		Status:  "init",
		Message: "",
	}
	metadata := &renterpb.Metadata{
		TimeCreate:  time.Now().UTC(),
		RenterId:    renterId,
		FileHash:    fileHash,
		ShardHashes: hashes,
	}
	ks := []string{fmt.Sprintf(session_status_key, f.peerId, f.Id),
		fmt.Sprintf(session_metadata_key, f.peerId, f.Id),
	}
	vs := []proto.Message{
		status,
		metadata,
	}
	err := ds.Batch(f.ds, ks, vs)
	fmt.Println("init111")
	if err == nil {
		go func(ctx context.Context, numShards int) {
			fmt.Println("init111, go routine")
			tick := time.Tick(5 * time.Second)
			for true {
				select {
				case <-tick:
					fmt.Println("init111, tick")
					completeNum, errorNum, err := f.GetCompleteShardsNum()
					fmt.Println("completeNum", completeNum, "errorNum", errorNum, "numShards", numShards)
					if err != nil {
						continue
					}
					if completeNum == numShards {
						err := f.ToSubmit()
						if err == nil {
							//return
						}
					} else if errorNum > 0 {
						err := f.ToError(errors.New("there are error shards"))
						if err == nil {
							return
						}
					}
				case <-ctx.Done():
					return
				}
			}
		}(f.ctx, len(hashes))
	} else {
		log.Error("err when init:", err)
	}
	return err
}

func (f *Session) error(err error) error {
	status := &renterpb.Status{
		Status:  "error",
		Message: err.Error(),
	}
	return ds.Save(f.ds, fmt.Sprintf(session_status_key, f.peerId, f.Id), status)
}

func (f *Session) GetMetadata() (*renterpb.Metadata, error) {
	mk := fmt.Sprintf(session_metadata_key, f.peerId, f.Id)
	md := &renterpb.Metadata{}
	err := ds.Get(f.ds, mk, md)
	if err == datastore.ErrNotFound {
		return md, nil
	}
	return md, err
}

func (f *Session) GetStatus() (*renterpb.Status, error) {
	sk := fmt.Sprintf(session_status_key, f.peerId, f.Id)
	st := &renterpb.Status{}
	err := ds.Get(f.ds, sk, st)
	if err == datastore.ErrNotFound {
		return st, nil
	}
	return st, err
}

func (f *Session) GetCompleteShardsNum() (int, int, error) {
	md, err := f.GetMetadata()
	var completeNum, errorNum int
	if err != nil {
		return 0, 0, err
	}
	for _, h := range md.ShardHashes {
		shard, err := GetShard(f.ctx, f.ds, f.peerId, f.Id, h)
		if err != nil {
			continue
		}
		status, err := shard.Status()
		if err != nil {
			continue
		}
		if status.Status == "complete" {
			completeNum++
		} else if status.Status == "error" {
			errorNum++
			return completeNum, errorNum, nil
		}
	}
	return completeNum, errorNum, nil
}
