package main

import (
	"log"

	"blob-chaincode/chaincode"

	"github.com/hyperledger/fabric-contract-api-go/contractapi"
)

func main() {
	blobChaincode, err := contractapi.NewChaincode(&chaincode.SmartContract{})
	if err != nil {
		log.Panicf("Error creating chaincode: %v", err)
	}

	if err := blobChaincode.Start(); err != nil {
		log.Panicf("Error starting chaincode: %v", err)
	}
}
