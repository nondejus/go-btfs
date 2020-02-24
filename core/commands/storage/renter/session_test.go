package renter

import (
	"context"
	"testing"
)

func TestFsm(t *testing.T) {
	f := NewFileSession(context.TODO())
	f.FileHash = "Qmdjob28Ur8jTxnjK8sH8X83ximMWFeHfhgBwzcDgXcTei"
	f.ToInit()
	f.ToSubmit()
	f.ToPay()
	f.ToGuard()
	f.ToComplete()
}
