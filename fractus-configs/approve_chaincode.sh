#!/bin/bash

# Needs to run only at one peer (peer 0) in each org, not all peers

# Assumes chaincode has already been packaged on the node running this script
CHAINCODE_ID=$(peer lifecycle chaincode calculatepackageid plain_string.tar.gz)
export CHAINCODE_ID

peer lifecycle chaincode approveformyorg -o 128.84.139.28:6050 --channelID mychannel --name plain_string --version 1 --package-id "${CHAINCODE_ID}" --sequence 1 --tls --cafile "${FABRIC_CFG_PATH}"/crypto-config/ordererOrganizations/example.com/orderers/orderer.example.com/tls/ca.crt
