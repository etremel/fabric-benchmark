package main

import (
	"log"

	"string-chaincode/chaincode"

	"github.com/hyperledger/fabric-contract-api-go/contractapi"
)

func main() {
	stringChaincode, err := contractapi.NewChaincode(&chaincode.SmartContract{})
	if err != nil {
		log.Panicf("Error creating chaincode: %v", err)
	}

	if err := stringChaincode.Start(); err != nil {
		log.Panicf("Error starting chaincode: %v", err)
	}
}
