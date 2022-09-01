package pbq

import (
	"context"
	"errors"
	"github.com/numaproj/numaflow/pkg/isb"
	"github.com/numaproj/numaflow/pkg/pbq/store"
	"github.com/numaproj/numaflow/pkg/shared/logging"
	"go.uber.org/zap"
	"time"
)

var COBErr error = errors.New("error while writing to pbq, pbq is closed")
var EOF error = errors.New("error while reading, EOF")

type PBQ struct {
	Store       store.Store
	output      chan *isb.Message
	cob         bool // cob to avoid panic in case writes happen after close of book
	partitionID string
	options     *store.Options
	isReplaying bool
	manager     *Manager
	log         *zap.SugaredLogger
}

// NewPBQ accepts size and store and returns new PBQ
func NewPBQ(ctx context.Context, partitionID string, persistentStore store.Store, pbqManager *Manager, options *store.Options) (*PBQ, error) {

	// output channel is buffered to support bulk reads
	p := &PBQ{
		Store:       persistentStore,
		output:      make(chan *isb.Message, options.BufferSize()),
		cob:         false,
		partitionID: partitionID,
		options:     options,
		manager:     pbqManager,
		log:         logging.FromContext(ctx).With("PBQ", partitionID),
	}

	return p, nil
}

// WriteFromISB writes message to pbq and persistent store
// We don't need a context here as this is invoked for every message.
func (p *PBQ) WriteFromISB(ctx context.Context, message *isb.Message) (writeErr error) {
	// if we are replaying records from the store, writes should be blocked
	for {
		if !p.isReplaying {
			break
		}
	}
	// if cob we should return
	if p.cob {
		p.log.Errorw("failed to write message to pbq, pbq is closed", zap.Any("partitionID", p.partitionID), zap.Any("header", message.Header))
		writeErr = COBErr
		return
	}
	// we need context to get out of blocking write
	select {
	case p.output <- message:
		writeErr = p.Store.WriteToStore(message)
		return
	case <-ctx.Done():
		// closing the output channel will not cause panic, since its inside select case
		close(p.output)
		writeErr = p.Store.Close()
	}
	return
}

//CloseOfBook closes output channel
func (p *PBQ) CloseOfBook() {
	close(p.output)
	p.cob = true
}

// CloseWriter is used by the writer to indicate close of context
// we should flush pending messages to store
func (p *PBQ) CloseWriter() (closeErr error) {
	closeErr = p.Store.Close()
	return
}

// ReadFromPBQ reads upto N messages (specified by size) from pbq
// if replay flag is set its reads messages from persisted store
func (p *PBQ) ReadFromPBQ(ctx context.Context, size int64) ([]*isb.Message, error) {
	var storeReadMessages []*isb.Message
	var err error
	var eof bool
	// replay flag is set, so we will consider store messages
	if p.isReplaying {
		storeReadMessages, eof, err = p.Store.ReadFromStore(size)
		// if store has no messages unset the replay flag
		if eof {
			p.isReplaying = false
		}
		if err != nil {
			p.log.Errorw("Error while replaying messages from store", zap.Any("partitionID", p.partitionID), zap.Any("store-type", p.options.PbqStoreType()), zap.Error(err))
			return nil, err
		}
		return storeReadMessages, nil
	}
	var pbqReadMessages []*isb.Message
	chanDrained := false

	readTimer := time.NewTimer(time.Second * time.Duration(p.options.ReadTimeoutSecs()))
	defer readTimer.Stop()

	readCount := 0
	// read n(size) messages from pbq, if context is canceled we should return,
	// to avoid infinite blocking we have timer
	for i := int64(0); i < size; i++ {
		select {
		case <-ctx.Done():
			return pbqReadMessages, ctx.Err()
		case <-readTimer.C:
			return pbqReadMessages, nil
		case msg, ok := <-p.output:
			if msg != nil {
				pbqReadMessages = append(pbqReadMessages, msg)
				readCount += 1
			}
			if !ok {
				chanDrained = true
			}
		}
		if chanDrained || size == int64(readCount) {
			break
		}
	}

	if chanDrained {
		return pbqReadMessages, EOF
	}
	return pbqReadMessages, nil
}

// CloseReader is used by the Reader to indicate that it has finished
// consuming the data from output channel
func (p *PBQ) CloseReader() (closeErr error) {
	return
}

// GC is invoked after the Reader (ProcessAndForward) has finished
// forwarding the output to ISB.
func (p *PBQ) GC() (gcErr error) {
	gcErr = p.Store.GC()
	p.Store = nil
	p.manager.Deregister(p.partitionID)
	return
}

// SetIsReplaying sets the replay flag
func (p *PBQ) SetIsReplaying(ctx context.Context, isReplaying bool) {
	p.isReplaying = isReplaying
}
