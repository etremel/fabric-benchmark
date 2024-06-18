package chaincode

import (
	"fmt"

	"github.com/hyperledger/fabric-contract-api-go/contractapi"
)

type SmartContract struct {
	contractapi.Contract
}

type Blob struct {
	Key  string
	Data []byte
}

func (s *SmartContract) PutData(ctx contractapi.TransactionContextInterface, key string, data []byte) error {
	return ctx.GetStub().PutState(key, data)
}

func (s *SmartContract) GetData(ctx contractapi.TransactionContextInterface, key string) (*Blob, error) {
	blobData, err := ctx.GetStub().GetState(key)
	if err != nil {
		return nil, fmt.Errorf("failed to read from world state: %v", err)
	}
	if blobData == nil {
		return nil, fmt.Errorf("object with key %s does not exist", key)
	}
	blobObject := Blob{
		Key:  key,
		Data: blobData,
	}
	return &blobObject, nil
}
