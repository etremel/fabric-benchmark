package chaincode

import (
	"fmt"

	"github.com/hyperledger/fabric-contract-api-go/contractapi"
)

type SmartContract struct {
	contractapi.Contract
}

func (s *SmartContract) PutString(ctx contractapi.TransactionContextInterface, key string, value string) error {
	return ctx.GetStub().PutState(key, []byte(value))
}

func (s *SmartContract) GetString(ctx contractapi.TransactionContextInterface, key string) (string, error) {
	rawData, err := ctx.GetStub().GetState(key)
	if err != nil {
		return "", fmt.Errorf("failed to read from world state: %v", err)
	}
	if rawData == nil {
		return "", fmt.Errorf("object with key %s does not exist", key)
	}
	dataString := string(rawData)
	return dataString, nil
}
