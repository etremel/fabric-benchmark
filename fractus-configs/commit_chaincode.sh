#!/bin/bash

# Needs to run on only one peer of one organization

peer lifecycle chaincode commit -o 128.84.139.28:6050 --channelID mychannel --name plain_string --version 1 --sequence 1 --tls --cafile ${FABRIC_CFG_PATH}/crypto-config/ordererOrganizations/example.com/orderers/orderer.example.com/tls/ca.crt --peerAddresses 128.84.139.25:7051 --tlsRootCertFiles ${FABRIC_CFG_PATH}/crypto-config/peerOrganizations/org1.example.com/peers/peer0.org1.example.com/tls/ca.crt  --peerAddresses 128.84.139.22:7051 --tlsRootCertFiles ${FABRIC_CFG_PATH}/crypto-config/peerOrganizations/org2.example.com/peers/peer0.org2.example.com/tls/ca.crt
