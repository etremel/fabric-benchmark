#!/usr/bin/env sh

# Needs to be targeted at all peers of both organizations, so they all have the chaincode installed

peer lifecycle chaincode package plain_string.tar.gz --path ../../fabric-benchmark/string-chaincode --lang golang --label plain_string_1
peer lifecycle chaincode install plain_string.tar.gz