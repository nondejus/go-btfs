package ds

import (
	coremock "github.com/TRON-US/go-btfs/core/mock"
	"github.com/stretchr/testify/assert"
	renterpb "github.com/tron-us/go-btfs-common/protos/btfs/renter"
	"testing"
	"time"
)

func TestSaveGet(t *testing.T) {
	node, err := coremock.NewMockNode()
	if err != nil {
		t.Fatal(err)
	}

	shardHashes := make([]string, 0)
	shardHashes = append(shardHashes, "Qm1")
	shardHashes = append(shardHashes, "Qm2")
	shardHashes = append(shardHashes, "Qm3")
	current := time.Now().UTC()
	md := &renterpb.Metadata{
		TimeCreate:  current,
		RenterId:    node.Identity.String(),
		FileHash:    "Qm123",
		ShardHashes: shardHashes,
	}
	err = Save(node.Repo.Datastore(), "ds.datastore.test", md)
	if err != nil {
		t.Fatal(err)
	}
	newMd := &renterpb.Metadata{}
	err = Get(node.Repo.Datastore(), "ds.datastore.test", newMd)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, md, newMd)
}
