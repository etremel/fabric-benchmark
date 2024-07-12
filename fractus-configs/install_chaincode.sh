#!/usr/bin/env sh
#
# SPDX-License-Identifier: Apache-2.0
#

peer lifecycle chaincode package plain_string.tar.gz --path ../../fabric-benchmark/string-chaincode --lang golang --label plain_string_1
peer lifecycle chaincode install plain_string.tar.gz

# Set the CHAINCODE_ID from the created chaincode package
CHAINCODE_ID=$(peer lifecycle chaincode calculatepackageid plain_string.tar.gz)
export CHAINCODE_ID

peer lifecycle chaincode approveformyorg -o 128.84.139.28:6050 --channelID mychannel --name plain_string --version 1 --package-id "${CHAINCODE_ID}" --sequence 1 --tls --cafile "${FABRIC_CFG_PATH}"/crypto-config/ordererOrganizations/example.com/orderers/orderer.example.com/tls/ca.crt 
