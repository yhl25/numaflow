package pbq

import (
	"context"
	"sync"
	"testing"
	"time"

	dfv1 "github.com/numaproj/numaflow/pkg/apis/numaflow/v1alpha1"
	"github.com/numaproj/numaflow/pkg/isb"
	"github.com/numaproj/numaflow/pkg/isb/testutils"
	"github.com/numaproj/numaflow/pkg/pbq/store"
	"github.com/stretchr/testify/assert"
)

func TestPBQ_WriteFromISB(t *testing.T) {

	// create a store of size 100 (it can store max 100 messages)
	storeSize := 100
	options := &store.StoreOptions{}
	_ = store.WithPbqStoreType(dfv1.InMemoryType)(options)
	_ = store.WithStoreSize(int64(storeSize))(options)
	ctx := context.Background()
	// create a pbq with buffer size 10
	buffSize := 10

	qManager, _ := NewManager(ctx, WithChannelBufferSize(int64(buffSize)),
		WithPBQStoreOptions(store.WithPbqStoreType(dfv1.InMemoryType), store.WithStoreSize(int64(storeSize))))

	// write 10 isb messages to persisted store
	msgCount := 10
	startTime := time.Now()
	writeMessages := testutils.BuildTestWriteMessages(int64(msgCount), startTime)

	pq, err := qManager.CreateNewPBQ(ctx, "partition-10")
	assert.NoError(t, err)

	for _, msg := range writeMessages {
		err := pq.Write(ctx, &msg)
		assert.NoError(t, err)
	}
	pq.CloseOfBook()

	// check if the messages are persisted in store
	pbqCh := pq.ReadCh()
	var storeMessages = make([]*isb.Message, 0)
	for msg := range pbqCh {
		storeMessages = append(storeMessages, msg)
	}
	assert.Len(t, storeMessages, msgCount)

	// this means we successfully wrote 10 messages to pbq
}

func TestPBQ_ReadFromPBQ(t *testing.T) {
	// create a store of size 100 (it can store max 100 messages)
	storeSize := 100
	// create a pbq with buffer size 10
	buffSize := 10

	ctx := context.Background()

	qManager, _ := NewManager(ctx, WithChannelBufferSize(int64(buffSize)), WithReadTimeout(1*time.Second),
		WithPBQStoreOptions(store.WithPbqStoreType(dfv1.InMemoryType), store.WithStoreSize(int64(storeSize))))

	// write 10 isb messages to persisted store
	msgCount := 10
	startTime := time.Now()
	writeMessages := testutils.BuildTestWriteMessages(int64(msgCount), startTime)

	pq, err := qManager.CreateNewPBQ(ctx, "partition-12")
	assert.NoError(t, err)

	for _, msg := range writeMessages {
		err := pq.Write(ctx, &msg)
		assert.NoError(t, err)
	}

	pq.CloseOfBook()

	var readMessages []*isb.Message

	for msg := range pq.ReadCh() {
		readMessages = append(readMessages, msg)
	}
	// number of messages written should be equal to number of messages read
	assert.Len(t, readMessages, msgCount)
	err = pq.GC()
	assert.NoError(t, err)
}

func TestPBQ_ReadWrite(t *testing.T) {
	// create a store of size 100 (it can store max 100 messages)
	storeSize := 100
	// create a pbq with buffer size 5
	buffSize := 5

	ctx := context.Background()

	qManager, _ := NewManager(ctx, WithChannelBufferSize(int64(buffSize)), WithReadTimeout(1*time.Second),
		WithPBQStoreOptions(store.WithPbqStoreType(dfv1.InMemoryType), store.WithStoreSize(int64(storeSize))))

	// write 10 isb messages to persisted store
	msgCount := 10
	startTime := time.Now()
	writeMessages := testutils.BuildTestWriteMessages(int64(msgCount), startTime)

	pq, err := qManager.CreateNewPBQ(ctx, "partition-13")
	assert.NoError(t, err)

	var readMessages []*isb.Message
	// run a parallel go routine which reads from pbq
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
	readLoop:
		for {
			select {
			case msg, ok := <-pq.ReadCh():
				if msg != nil {
					readMessages = append(readMessages, msg)
				}
				if !ok {
					break readLoop
				}
			case <-ctx.Done():
				break readLoop
			}
		}
		wg.Done()
	}()

	for _, msg := range writeMessages {
		err := pq.Write(ctx, &msg)
		assert.NoError(t, err)
	}
	pq.CloseOfBook()

	wg.Wait()
	// count of messages read by parallel go routine should be equal to produced messages
	assert.Len(t, readMessages, msgCount)

}

func Test_PBQReadWithCanceledContext(t *testing.T) {
	// create a store of size 100 (it can store max 100 messages)
	storeSize := 100
	//create a pbq with buffer size 10
	bufferSize := 10
	var err error
	var qManager *Manager

	ctx := context.Background()

	qManager, err = NewManager(ctx, WithChannelBufferSize(int64(bufferSize)), WithReadTimeout(1*time.Second),
		WithPBQStoreOptions(store.WithPbqStoreType(dfv1.InMemoryType), store.WithStoreSize(int64(storeSize))))

	assert.NoError(t, err)

	//write 10 isb messages to persisted store
	msgCount := 10
	startTime := time.Now()
	writeMessages := testutils.BuildTestWriteMessages(int64(msgCount), startTime)

	var pq ReadWriteCloser
	pq, err = qManager.CreateNewPBQ(ctx, "partition-14")
	assert.NoError(t, err)

	var readMessages []*isb.Message
	// run a parallel go routine which reads from pbq
	var wg sync.WaitGroup
	wg.Add(1)

	childCtx, cancelFn := context.WithCancel(ctx)

	go func() {
	readLoop:
		for {
			select {
			case msg, ok := <-pq.ReadCh():
				if msg != nil {
					readMessages = append(readMessages, msg)
				}
				if !ok {
					break readLoop
				}
			case <-childCtx.Done():
				break readLoop
			}
		}
		wg.Done()
	}()

	for _, msg := range writeMessages {
		err := pq.Write(ctx, &msg)
		assert.NoError(t, err)
	}

	time.Sleep(1 * time.Second)
	//since we are closing the context, it should not block
	cancelFn()
	pq.CloseOfBook()

	wg.Wait()
	assert.Len(t, readMessages, 10)
}

func TestPBQ_WriteWithStoreFull(t *testing.T) {

	// create a store of size 100 (it can store max 100 messages)
	storeSize := 100
	// create a pbq with buffer size 101
	buffSize := 101
	var qManager *Manager
	var err error
	ctx := context.Background()

	qManager, err = NewManager(ctx, WithChannelBufferSize(int64(buffSize)), WithReadTimeout(1*time.Second),
		WithPBQStoreOptions(store.WithPbqStoreType(dfv1.InMemoryType), store.WithStoreSize(int64(storeSize))))
	assert.NoError(t, err)

	// write 101 isb messages to persisted store, but the store size is 100
	msgCount := 101
	startTime := time.Now()
	writeMessages := testutils.BuildTestWriteMessages(int64(msgCount), startTime)

	var pq ReadWriteCloser
	pq, err = qManager.CreateNewPBQ(ctx, "partition-10")
	assert.NoError(t, err)

	for _, msg := range writeMessages {
		err = pq.Write(ctx, &msg)
	}
	pq.CloseOfBook()

	assert.Error(t, err, store.WriteStoreFullErr)
	// check if the messages are persisted in store
	pbqCh := pq.ReadCh()
	var storeMessages = make([]*isb.Message, 0)
	for msg := range pbqCh {
		storeMessages = append(storeMessages, msg)
	}
	assert.Len(t, storeMessages, buffSize)
}
