package main

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/hashgraph/hedera-sdk-go"
	hb "github.com/hashgraph/txmapper/proto"
)

type blockCipher struct {
	Type string
	Key  string
}

type hcsConfig struct {
	MirrorNodeAddress string
	BlockCipher       blockCipher
}

type hcsApplicationMessage struct {
	message   []byte
	responses []*consensusTopicResponse
}

type chunkHolder struct {
	count  int32
	total  int32
	chunks []*hb.ApplicationMessageChunk
	resps  []*consensusTopicResponse
}

type consensusTopicResponse struct {
	ConsensusTimestamp time.Time
	RunningHash        []byte
	SequenceNumber     uint64
}

func reassembleHcsMessages(hcsConfig *hcsConfig, topicID hedera.ConsensusTopicID, start time.Time, end time.Time) ([]*hcsApplicationMessage, error) {
	var messages []*hcsApplicationMessage
	client, err := hedera.NewMirrorClient(hcsConfig.MirrorNodeAddress)
	if err != nil {
		return messages, err
	}

	// TODO: handle decryption
	pending := make(map[string]*chunkHolder)
	done := make(chan struct{})
	onNext := func(resp hedera.MirrorConsensusTopicResponse) {
		var holder *chunkHolder
		var key string
		defer func() {
			if holder != nil && holder.count == holder.total {
				delete(pending, key)
			}
		}()

		chunk := &hb.ApplicationMessageChunk{}
		if err := proto.Unmarshal(resp.Message, chunk); err != nil {
			// log the error and skip
			return
		}

		messageID := chunk.ApplicationMessageId
		key = fmt.Sprintf("%s%s", hex.EncodeToString(messageID.Metadata.Value), messageID.ValidStart)
		holder, ok := pending[key]
		if !ok {
			holder = &chunkHolder{
				total:  chunk.ChunksCount,
				chunks: make([]*hb.ApplicationMessageChunk, chunk.ChunksCount),
				resps:  make([]*consensusTopicResponse, chunk.ChunksCount),
			}
			pending[key] = holder
		}

		if chunk.ChunkIndex >= chunk.ChunksCount || holder.chunks[chunk.ChunkIndex] != nil {
			return
		}
		holder.chunks[chunk.ChunkIndex] = chunk
		holder.resps[chunk.ChunkIndex] = &consensusTopicResponse{
			ConsensusTimestamp: resp.ConsensusTimeStamp,
			RunningHash:        resp.RunningHash,
			SequenceNumber:     resp.SequenceNumber,
		}
		holder.count++
		logger.Debugf("received a chunk for '%s', size - %d, index - %d, total - %d", key, len(chunk.MessageChunk), chunk.ChunkIndex, chunk.ChunksCount)

		if holder.count == holder.total {
			var data []byte
			for _, chunk := range holder.chunks {
				data = append(data, chunk.MessageChunk...)
			}

			appMsg := &hb.ApplicationMessage{}
			if err = proto.Unmarshal(data, appMsg); err != nil {
				logger.Errorf("failed to unmarshal data to ApplicationMessage: %s", err)
				return
			}

			hcsAppMsg := &hcsApplicationMessage{
				message:   appMsg.BusinessProcessMessage,
				responses: holder.resps,
			}
			messages = append(messages, hcsAppMsg)
			logger.Debugf("reassembled a business process message of %d bytes", len(hcsAppMsg.message))
		}
	}
	onError := func(err error) {
		select {
		case <-done:
			// do nothing if already closed
		default:
			logger.Errorf("subscription error received - %s, stop handling consensus topic responses", err)
			close(done)
		}
	}
	handle, err := hedera.NewMirrorConsensusTopicQuery().
		SetTopicID(topicID).
		SetStartTime(start).
		SetEndTime(end).
		Subscribe(client, onNext, onError)
	if err != nil {
		return messages, err
	}

	<-done
	handle.Unsubscribe()

	logger.Debugf("all consensus topic responses processed, %d messages reassembled", len(messages))
	return messages, nil
}
