package renter

import (
	"context"
	"testing"
)

func TestFsm(t *testing.T) {
	f := NewFileSession(context.TODO())
	f.ToInit()
	f.ToSubmit()
	f.ToPay()
	f.ToGuard()
	f.ToComplete()
}
