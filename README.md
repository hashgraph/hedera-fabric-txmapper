# Hedera Fabric TXMapper

txmapper is a command-line tool to map a Hyperledger Fabric transaction to the corresponding list of HCS messages.
With a valid Fabric transaction ID and proper configuration, the tool shows the channel's HCS topic ID and `ConsensusTimestamp`,
`RunningHash` and `SequenceNumber` of the corresponding HCS messages.

## Install

```
GO111MODULE=on go get github.com/hashgraph/hedera-fabric-txmapper/txmapper
```

## Configuration

[config.yaml](txmapper/config.yaml) provides a configuration template. The format of `fabric` section of the configuration is defined by
[fabric-sdk-go](https://github.com/hyperledger/fabric-sdk-go). The txmapper tool requires the configuration of a single peer node, the organization the peer belongs to and the channel the peer node is a member of. Please note the filename of the private key of the users
in the organization is required in the form of \<SKI\>\_sk, where SKI is the SHA256 sum of the public key parameters.

## Sample Output

```
$ ./txmapper --config config.yaml --channelID mychannel ab3a1b7796c7690a2f1db2e99a7d6744c1d361a2761cb3422fdef49f25a7d35d
{
  "TopicID": "0.0.45550",
  "Mapping": {
    "ab3a1b7796c7690a2f1db2e99a7d6744c1d361a2761cb3422fdef49f25a7d35d": [
      {
        "ConsensusTimestamp": "2020-05-14T15:07:35.60048-05:00",
        "RunningHash": "8v8Z5z8uZNhTnm1yAm3BrQBUN3t1rg087QlwABVqqUvzKgok5gc7dAHbWNefqHmy",
        "SequenceNumber": 24
      },
      {
        "ConsensusTimestamp": "2020-05-14T15:07:35.600480002-05:00",
        "RunningHash": "5hqRLRJu9HAIVH/NZV+XGsoTiB4oxpKzN/vqT7yRQvbzvQehKL2UCH0WS6Et43+i",
        "SequenceNumber": 25
      }
    ]
  }
}
```

The JSON output can be saved to a file with the `--output` command line flag.
