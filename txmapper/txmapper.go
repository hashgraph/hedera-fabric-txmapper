package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/golang/protobuf/ptypes"
	"github.com/golang/protobuf/ptypes/timestamp"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	logger *log.Logger

	configFile   string
	outputFile   string
	channelID    string
	user         string
	loggingLevel string
)

func main() {
	logger = log.New()
	logger.SetFormatter(&log.TextFormatter{FullTimestamp: true})

	cmd := &cobra.Command{
		Use:   "txmapper tx_id",
		Short: "txmapper maps a fabric transaction to the corresponding HCS messages",
		Long:  `A tool to map a fabric transaction by its transaction ID to the corresponding HCS messages`,
		PreRun: func(cmd *cobra.Command, args []string) {
			lvl, err := log.ParseLevel(loggingLevel)
			if err != nil {
				logger.Errorf("invalid logging level: %s", loggingLevel)
			} else {
				logger.SetLevel(lvl)
			}
		},
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.Errorf("exact one transaction id is required")
			}
			return nil
		},
		RunE:          getMapping,
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.Flags().StringVar(&configFile, "config", "", "the path of the config.yaml file")
	cmd.MarkFlagRequired("config")
	cmd.MarkFlagFilename("config")
	cmd.Flags().StringVar(&outputFile, "output", "", "output file, if not set, output is written to stdout")
	cmd.Flags().StringVar(&channelID, "channelID", "", "fabric channel ID")
	cmd.MarkFlagRequired("channelID")
	cmd.Flags().StringVar(&user, "user", "Admin", "user to run the fabric requests")
	cmd.Flags().StringVarP(&loggingLevel, "logging-level", "l", "ERROR", "logging level - ERROR, WARN, INFO, DEBUG")

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

type outputItem struct {
	Operation string
	Key       string
	Value     string
}

type action struct {
	Input  []string
	Output []outputItem
}

type txInfo struct {
	HcsMessageMeta []*consensusTopicResponse
	Actions        []action
}

type result struct {
	TopicID string
	Mapping map[string]txInfo
}

func getMapping(cmd *cobra.Command, args []string) (err error) {
	defer func() {
		if err != nil {
			logger.Error(err)
		}
	}()

	fabricConfig, hcsConfig, err := loadConfig(configFile)
	if err != nil {
		return err
	}

	fabricClient, err := newFabricClient(fabricConfig)
	if err != nil {
		return err
	}
	defer fabricClient.Close()

	topicID, err := fabricClient.GetConsensusTopicIDForChannel(channelID)
	if err != nil {
		return err
	}

	txID := args[0]
	start, end, err := getConsensusTimestamps(fabricClient, channelID, txID)
	if err != nil {
		return err
	}

	hcsAppMsgs, err := reassembleHcsMessages(hcsConfig, topicID, start, end)
	if err != nil {
		return err
	}

	// find the matching original transaction
	result := &result{
		TopicID: topicID.String(),
		Mapping: make(map[string]txInfo),
	}
	for _, hcsAppMsg := range hcsAppMsgs {
		payload, thisTxID, err := getTxPayloadIDFromHcsMsg(hcsAppMsg.message)
		if err != nil {
			logger.Errorf("failed to get txID from a reassembled message: %s", err)
			continue
		}

		if thisTxID == txID {
			// found the match
			actions, err := getActionsFromTxPayload(payload)
			if err != nil {
				logger.Error(err)
			}
			result.TopicID = topicID.String()
			result.Mapping[txID] = txInfo{
				HcsMessageMeta: hcsAppMsg.responses,
				Actions:        actions,
			}
			return writeJSONOutput(result, outputFile)
		}
	}

	return errors.Errorf("no matching transaction found")
}

func getConsensusTimestamps(fabricClient *fabricClient, channelID, txID string) (start, end time.Time, err error) {
	curHcsMetadata, blockNumber, err := fabricClient.GetBlockMetadataByTxID(channelID, txID)
	if err != nil {
		return start, end, err
	}

	var startTimestamp, endTimestamp *timestamp.Timestamp
	if !timestampEqual(curHcsMetadata.LastConsensusTimestampPersisted, curHcsMetadata.LastChunkFreeConsensusTimestampPersisted) {
		startTimestamp = curHcsMetadata.LastChunkFreeConsensusTimestampPersisted
		endTimestamp = curHcsMetadata.LastConsensusTimestampPersisted
	} else {
		if blockNumber > 0 {
			prevHcsMetadata, err := fabricClient.GetBlockMetadataByBlockNumber(channelID, blockNumber-1)
			if err != nil {
				return start, end, errors.Wrapf(err, "failed to get block metadata for previous block: %s", err)
			}
			startTimestamp = prevHcsMetadata.LastConsensusTimestampPersisted
		}
		endTimestamp = curHcsMetadata.LastConsensusTimestampPersisted
	}

	if startTimestamp == nil {
		start = time.Unix(0, 0) // epoch
	} else {
		start, err = ptypes.Timestamp(startTimestamp)
		if err != nil {
			return start, end, err
		}
		start = start.Add(time.Nanosecond)
	}

	end, err = ptypes.Timestamp(endTimestamp)
	return start, end, err
}

func timestampEqual(t1 *timestamp.Timestamp, t2 *timestamp.Timestamp) bool {
	if t1 == t2 {
		return true
	}

	if t1 == nil || t2 == nil {
		return false
	}

	if (t1.Seconds == t2.Seconds) && (t1.Nanos == t2.Nanos) {
		return true
	}

	return false
}

func writeJSONOutput(result *result, outputFile string) error {
	jsonOut, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}

	if outputFile != "" {
		f, err := os.Create(outputFile)
		if err != nil {
			return err
		}
		defer f.Close()

		if _, err = f.Write(jsonOut); err != nil {
			return errors.Wrapf(err, "failed to write json to file %s", outputFile)
		}
	} else {
		fmt.Println(string(jsonOut))
	}

	return nil
}
