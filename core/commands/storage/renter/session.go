package renter

import (
	"context"
	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/looplab/fsm"
	"time"
)

var (
	timeouts = map[string]time.Duration{
		"init":   1 * time.Second,
		"submit": 1 * time.Second,
		"pay":    1 * time.Second,
		"guard":  1 * time.Second,
	}
)

type FileSession struct {
	Id        string
	Filehash  string
	RenterPid peer.ID
	ctx       context.Context
	step      chan interface{}
	fsm       *fsm.FSM
}

func NewFileSession(ctx context.Context) *FileSession {
	f := &FileSession{
		Id:   uuid.New().String(),
		ctx:  ctx,
		step: make(chan interface{}),
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

func (f *FileSession) enterState(e *fsm.Event) {
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
}

func (f *FileSession) ToInit() error {
	return f.fsm.Event("toInit")
}

func (f *FileSession) ToSubmit() error {
	f.step <- struct{}{}
	return f.fsm.Event("toSubmit")
}

func (f *FileSession) ToPay() error {
	f.step <- struct{}{}
	return f.fsm.Event("toPay")
}

func (f *FileSession) ToGuard() error {
	f.step <- struct{}{}
	return f.fsm.Event("toGuard")
}

func (f *FileSession) ToComplete() error {
	f.step <- struct{}{}
	return f.fsm.Event("toComplete")
}

func (f *FileSession) Timeout() error {
	return f.fsm.Event("toError")
}
