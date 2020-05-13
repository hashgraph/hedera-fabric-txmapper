package main

import (
	"fmt"

	"github.com/golang/protobuf/proto"
	"github.com/hashgraph/hedera-sdk-go"
	hb "github.com/hashgraph/txmapper/protodef"
	"github.com/hyperledger/fabric-protos-go/common"
	"github.com/hyperledger/fabric-protos-go/orderer"
	"github.com/hyperledger/fabric-sdk-go/pkg/client/ledger"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/context"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/core"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/fab"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/msp"
	contextImpl "github.com/hyperledger/fabric-sdk-go/pkg/context"
	"github.com/hyperledger/fabric-sdk-go/pkg/core/config"
	cryptosuiteimpl "github.com/hyperledger/fabric-sdk-go/pkg/core/cryptosuite/bccsp/multisuite"
	"github.com/hyperledger/fabric-sdk-go/pkg/fabsdk"
	"github.com/hyperledger/fabric-sdk-go/pkg/fabsdk/factory/defcore"
	"github.com/pkg/errors"
)

func newFabricClient(fabricConfig []byte) (*fabricClient, error) {
	configProvider := config.FromRaw(fabricConfig, "yaml")
	sdk, err := fabsdk.New(configProvider, fabsdk.WithCorePkg(&cryptoSuiteProviderFactory{}))
	if err != nil {
		return nil, errors.Errorf("failed to initialize fabric SDK: %s", err)
	}

	ctx, err := sdk.Context()()
	if err != nil {
		return nil, errors.Errorf("error creating client context: %s", err)
	}

	endpointConfig := ctx.EndpointConfig()
	if len(endpointConfig.NetworkPeers()) == 0 {
		return nil, errors.Errorf("require at least one peer with corresponding organization in config.yaml")
	}

	var userIdentity msp.SigningIdentity
	var peerURL string
	for orgName := range endpointConfig.NetworkConfig().Organizations {
		manager, ok := ctx.IdentityManager(orgName)
		if !ok {
			logger.Debugf("failed to get identity manager for org '%s'", orgName)
			continue
		}

		identity, err := manager.GetSigningIdentity(user)
		if err == nil {
			peersConfig, ok := endpointConfig.PeersConfig(orgName)
			if !ok {
				logger.Debugf("identity loaded for '%s', but no peersConfig found", orgName)
				continue
			}

			userIdentity = identity
			peerURL = peersConfig[0].URL
			logger.Debugf("loaded signing identity for user '%s', peer '%s' in org '%s'", user, peerURL, orgName)
			break
		}
	}
	if userIdentity == nil {
		return nil, errors.Errorf("unable to get signing identity for user '%s'", user)
	}

	logger.Debugf("created fabricClient with identity '%s' and peer '%s' for '%s'", user, peerURL, userIdentity.Identifier().MSPID)
	return &fabricClient{
		sdk:          sdk,
		userIdentity: userIdentity,
		session:      sdk.Context(fabsdk.WithIdentity(userIdentity)),
		peerURL:      peerURL,
	}, nil
}

type fabricClient struct {
	sdk          *fabsdk.FabricSDK
	userIdentity msp.SigningIdentity
	session      context.ClientProvider
	peerURL      string
}

func (fc *fabricClient) GetConsensusTopicIDForChannel(channelID string) (hedera.ConsensusTopicID, error) {
	topicID := hedera.ConsensusTopicID{}
	client, err := ledger.New(fc.channelProvider(channelID))
	if err != nil {
		return topicID, err
	}

	block, err := client.QueryConfigBlock(ledger.WithTargetEndpoints(fc.peerURL))
	if err != nil {
		return topicID, errors.Errorf("failed to retrieve current config block for channel %s - %s", channelID, err)
	}

	// get envelope
	envelope := &common.Envelope{}
	if err = proto.Unmarshal(block.Data.Data[0], envelope); err != nil {
		return topicID, errors.Errorf("failed to get envelope from config block: %s", err)
	}

	payload := &common.Payload{}
	if err = proto.Unmarshal(envelope.Payload, payload); err != nil {
		return topicID, errors.Errorf("failed to extract payload of envelope: %s", err)
	}

	configEnvelope := &common.ConfigEnvelope{}
	if err = proto.Unmarshal(payload.Data, configEnvelope); err != nil {
		return topicID, errors.Errorf("failed to extract config envelope: %s", err)
	}

	ordererGroup := configEnvelope.Config.ChannelGroup.Groups["Orderer"]
	if ordererGroup == nil {
		return topicID, fmt.Errorf("no Orderer group in root Groups")
	}

	if _, ok := ordererGroup.Values["ConsensusType"]; !ok {
		return topicID, fmt.Errorf("no ConsensusType in ordererGroup.Values map")
	}

	consensusType := &orderer.ConsensusType{}
	if err = proto.Unmarshal(ordererGroup.Values["ConsensusType"].Value, consensusType); err != nil {
		return topicID, errors.Errorf("failed to unmarshal ConsensusType: %s", err)
	}

	if consensusType.Type != "hcs" {
		return topicID, fmt.Errorf("unsupported consensus type %s", consensusType.Type)
	}

	configMetadata := &hb.HcsConfigMetadata{}
	if err = proto.Unmarshal(consensusType.Metadata, configMetadata); err != nil {
		return topicID, fmt.Errorf("failed to unmarshal HCS config metadata: %s", err)
	}

	if topicID, err = hedera.TopicIDFromString(configMetadata.TopicId); err != nil {
		return topicID, err
	}

	logger.Debugf("consensus topic ID for channel '%s' is %s", channelID, topicID)
	return topicID, nil
}

func (fc *fabricClient) GetBlockMetadataByTxID(channelID, txID string) (*hb.HcsMetadata, uint64, error) {
	client, err := ledger.New(fc.channelProvider(channelID))
	if err != nil {
		return nil, 0, err
	}

	block, err := client.QueryBlockByTxID(fab.TransactionID(txID), ledger.WithTargetEndpoints(fc.peerURL))
	if err != nil {
		return nil, 0, err
	}

	hcsMetadata, err := getHcsMetadataFromBlock(block)
	return hcsMetadata, block.Header.Number, err
}

func (fc *fabricClient) GetBlockMetadataByBlockNumber(channelID string, blockNumber uint64) (*hb.HcsMetadata, error) {
	client, err := ledger.New(fc.channelProvider(channelID))
	if err != nil {
		return nil, err
	}

	block, err := client.QueryBlock(blockNumber, ledger.WithTargetEndpoints(fc.peerURL))
	if err != nil {
		return nil, err
	}

	return getHcsMetadataFromBlock(block)
}

func (fc *fabricClient) Close() {
	if fc.sdk != nil {
		fc.sdk.Close()
	}
}

func (fc *fabricClient) channelProvider(channelID string) context.ChannelProvider {
	return func() (context.Channel, error) {
		return contextImpl.NewChannel(fc.session, channelID)
	}
}

// cryptoSuiteProviderFactory will provide custom cryptosuite (bccsp.BCCSP)
type cryptoSuiteProviderFactory struct {
	defcore.ProviderFactory
}

// CreateCryptoSuiteProvider returns a new default implementation of BCCSP
func (f *cryptoSuiteProviderFactory) CreateCryptoSuiteProvider(config core.CryptoSuiteConfig) (core.CryptoSuite, error) {
	return cryptosuiteimpl.GetSuiteByConfig(config)
}

func getHcsMetadataFromBlock(block *common.Block) (*hb.HcsMetadata, error) {
	rawMetadata := block.Metadata.Metadata[common.BlockMetadataIndex_SIGNATURES]
	metadata := &common.Metadata{}
	if err := proto.Unmarshal(rawMetadata, metadata); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal rawMetadata: %s", err)
	}

	ordererMetadata := &common.OrdererBlockMetadata{}
	if err := proto.Unmarshal(metadata.Value, ordererMetadata); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal metadata.Value: %s", err)
	}

	metadata.Reset()
	if err := proto.Unmarshal(ordererMetadata.ConsenterMetadata, metadata); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal ordererMetadata.ConsenterMetadata: %s", err)
	}

	hcsMetadata := &hb.HcsMetadata{}
	if err := proto.Unmarshal(metadata.Value, hcsMetadata); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal metadata.Value to HcsMetadata: %s", err)
	}

	return hcsMetadata, nil
}

func getTxIDFromHcsMsg(rawHcsMsg []byte) (string, error) {
	hcsMsg := &hb.HcsMessage{}
	if err := proto.Unmarshal(rawHcsMsg, hcsMsg); err != nil {
		return "", err
	}

	_, ok := hcsMsg.Type.(*hb.HcsMessage_Regular)
	if !ok {
		return "", errors.Errorf("not a fabric transaction, since it's not a regular HcsMessage")
	}

	envelope := &common.Envelope{}
	if err := proto.Unmarshal(hcsMsg.GetRegular().Payload, envelope); err != nil {
		return "", err
	}

	if envelope.Payload == nil {
		return "", errors.Errorf("not a valid envelope")
	}

	payload := &common.Payload{}
	if err := proto.Unmarshal(envelope.Payload, payload); err != nil {
		return "", err
	}

	header := &common.ChannelHeader{}
	if err := proto.Unmarshal(payload.Header.ChannelHeader, header); err != nil {
		return "", err
	}

	logger.Debugf("extracted txID - %s", header.TxId)
	return header.TxId, nil
}
