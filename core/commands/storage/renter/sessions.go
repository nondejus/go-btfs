package renter

import (
	"context"
	"fmt"
	"github.com/TRON-US/go-btfs/core/commands/storage/ds"
	"github.com/google/uuid"
	"github.com/ipfs/go-datastore"
	"github.com/looplab/fsm"
	renterpb "github.com/tron-us/go-btfs-common/protos/btfs/renter"
	"github.com/tron-us/protobuf/proto"
	"time"
)

const (
	session_metadata_key  = "/peers/%s/v0.0.1/renter/sessions/%s/metadata"
	session_status_key    = "/peers/%s/v0.0.1/renter/sessions/%s/status"
	session_contracts_key = "/peers/%s/v0.0.1/renter/sessions/%s/contracts"
)

type Session struct {
	Id     string
	peerId string
	ctx    context.Context
	step   chan interface{}
	fsm    *fsm.FSM
	ds     datastore.Datastore
}

func NewSession(ctx context.Context, ds datastore.Datastore, peerId string) *Session {
	f := &Session{
		Id:     uuid.New().String(),
		ctx:    ctx,
		ds:     ds,
		peerId: peerId,
		step:   make(chan interface{}),
	}
	f.fsm = fsm.NewFSM("new",
		fsm.Events{
			{Name: "toInit", Src: []string{"new"}, Dst: "init"},
			{Name: "toSubmit", Src: []string{"init"}, Dst: "submit"},
			{Name: "toPay", Src: []string{"submit"}, Dst: "pay"},
			{Name: "toGuard", Src: []string{"pay"}, Dst: "guard"},
			{Name: "toComplete", Src: []string{"guard"}, Dst: "complete"},
			{Name: "toError", Src: []string{"init", "submit", "pay", "guard"}, Dst: "error"},
		},
		fsm.Callbacks{
			"enter_state": f.enterState,
		},
	)
	return f
}

func (f *Session) enterState(e *fsm.Event) {
	t, ok := timeouts[e.Dst]
	if ok {
		go func() {
			tick := time.Tick(t)
			select {
			case <-f.step:
				break
			case <-tick:
				f.Timeout()
				break
			}
		}()
	}
	switch e.Dst {
	case "init":
		f.init(e.Args[0].(string), e.Args[1].(string))
	}
}

func (f *Session) ToInit(renterId string, fileHash string) error {
	return f.fsm.Event("toInit", renterId, fileHash)
}

func (f *Session) ToSubmit() error {
	f.step <- struct{}{}
	return f.fsm.Event("toSubmit")
}

func (f *Session) ToPay() error {
	f.step <- struct{}{}
	return f.fsm.Event("toPay")
}

func (f *Session) ToGuard() error {
	f.step <- struct{}{}
	return f.fsm.Event("toGuard")
}

func (f *Session) ToComplete() error {
	f.step <- struct{}{}
	return f.fsm.Event("toComplete")
}

func (f *Session) Timeout() error {
	return f.fsm.Event("toError")
}

func (f *Session) init(renterId string, fileHash string) error {
	status := &renterpb.Status{
		Status:  "init",
		Message: "",
	}
	metadata := &renterpb.Metadata{
		TimeCreate: time.Now().UTC(),
		RenterId:   renterId,
		FileHash:   fileHash,
	}
	ks := []string{fmt.Sprintf(session_status_key, f.peerId, f.Id),
		fmt.Sprintf(session_metadata_key, f.peerId, f.Id)}
	vs := []proto.Message{
		status,
		metadata,
	}
	return ds.Batch(f.ds, ks, vs)
}

func (f *Session) GetMetadata() (*renterpb.Metadata, error) {
	sk := fmt.Sprintf(session_status_key, f.peerId, f.Id)
	md := &renterpb.Metadata{}
	err := ds.Get(f.ds, sk, &renterpb.Metadata{})
	return md, err
}
