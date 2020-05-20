package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang/protobuf/proto"
	hb "github.com/hashgraph/hedera-fabric-txmapper/txmapper/protodef"
	"github.com/hashgraph/hedera-sdk-go"
	"github.com/pkg/errors"
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

func makeGCMCipher(config *blockCipher) (cipher.AEAD, error) {
	if config.Type != "aes-256-gcm" {
		return nil, errors.Errorf("unsupported block cipher type: %s", config.Type)
	}

	if config.Key == "" {
		return nil, errors.Errorf("no key provided")
	}

	rawKey, err := base64.StdEncoding.DecodeString(config.Key)
	if err != nil {
		return nil, errors.Wrap(err, "key string it not base64 encoded")
	}

	if len(rawKey) != 32 {
		return nil, errors.Errorf("requires 32-byte key for aes-256-gcm, got %d bytes", len(rawKey))
	}

	block, err := aes.NewCipher(rawKey)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create the AES block cipher")
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create the GCM cipher")
	}

	return gcm, nil
}

func reassembleHcsMessages(hcsConfig *hcsConfig, topicID hedera.ConsensusTopicID, start time.Time, end time.Time) ([]*hcsApplicationMessage, error) {
	var messages []*hcsApplicationMessage
	client, err := hedera.NewMirrorClient(hcsConfig.MirrorNodeAddress)
	if err != nil {
		return messages, err
	}

	gcmCipher, err := makeGCMCipher(&hcsConfig.BlockCipher)
	if err != nil {
		logger.Error(err)
	}

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

			if len(appMsg.EncryptionRandom) != 0 {
				if gcmCipher == nil {
					logger.Errorf("business process message is encrypted but no block cipher is configured, skip it...")
					return
				}

				plaintext, err := gcmCipher.Open(nil, appMsg.EncryptionRandom, appMsg.BusinessProcessMessage, nil)
				if err != nil {
					logger.Error("failed to decrypt business process message, skip it...")
					return
				}
				appMsg.BusinessProcessMessage = plaintext
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
