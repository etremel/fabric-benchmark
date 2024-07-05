package main

import (
	"bufio"
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"github.com/hyperledger/fabric-protos-go-apiv2/gateway"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

const (
	mspID        = "Org1MSP"
	cryptoPath   = "../../fabric-samples/test-network/organizations/peerOrganizations/org1.example.com"
	certPath     = cryptoPath + "/users/User1@org1.example.com/msp/signcerts"
	keyPath      = cryptoPath + "/users/User1@org1.example.com/msp/keystore"
	tlsCertPath  = cryptoPath + "/peers/peer0.org1.example.com/tls/ca.crt"
	peerEndpoint = "dns:///localhost:7051"
	gatewayPeer  = "peer0.org1.example.com"
)

func main() {
	verbose := flag.Bool("v", false, "Verbose mode")
	flag.Parse()
	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	} else {
		slog.SetLogLoggerLevel(slog.LevelInfo)
	}
	if len(flag.Args()) < 2 {
		fmt.Println("Error: Missing required arguments")
		fmt.Printf("Usage: %s [-v] <data size> <num messages>\n", os.Args[0])
		return
	}
	data_size_i64, parseErr := strconv.ParseInt(flag.Args()[0], 0, 0)
	if parseErr != nil {
		panic(fmt.Errorf("failed to parse argument 1 as an int: %w", parseErr))
	}
	num_updates_i64, parseErr := strconv.ParseInt(flag.Args()[1], 0, 0)
	if parseErr != nil {
		panic(fmt.Errorf("failed to parse argument 2 as an int: %w", parseErr))
	}
	// Annoyingly, ParseInt always returns an int64, which must be cast to the correct type
	test_data_size := int(data_size_i64)
	num_test_updates := int(num_updates_i64)

	// The gRPC client connection should be shared by all Gateway connections to this endpoint
	clientConnection := newGrpcConnection()
	defer clientConnection.Close()

	id := newIdentity()
	sign := newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}
	defer gw.Close()

	// Override default values for chaincode and channel name as they may differ in testing contexts.
	chaincodeName := "strings"
	if ccname := os.Getenv("CHAINCODE_NAME"); ccname != "" {
		chaincodeName = ccname
	}

	channelName := "mychannel"
	if cname := os.Getenv("CHANNEL_NAME"); cname != "" {
		channelName = cname
	}

	network := gw.GetNetwork(channelName)
	contract := network.GetContract(chaincodeName)

	const workload_size = 1024
	workloadObjects := generateWorkload(workload_size, test_data_size)
	putCommitChannel := make(chan *client.Commit, num_test_updates)
	doneChannel := make(chan bool)
	sendTimes := make([]time.Time, num_test_updates)
	commitTimes := make([]time.Time, num_test_updates)
	// Start a thread to receive commit pointers from the SubmitAsync calls and wait on them
	go collectStatuses(putCommitChannel, commitTimes, doneChannel)
	// Send a bunch of put updates, each targeting a random key, with the test data
	// Measure the total time taken and divide by the total number of bytes sent to get the throughput
	beginTime := time.Now()
	for i := range num_test_updates {
		testObjectKey := fmt.Sprintf("testKey_%d", rand.Intn(len(workloadObjects)))
		sendTimes[i] = time.Now()
		putCommitChannel <- putStringAsync(contract, testObjectKey, workloadObjects[testObjectKey])
	}
	// Wait for the collectStatuses thread to finish
	<-doneChannel
	endTime := time.Now()
	totalDuration := endTime.Sub(beginTime)
	throughputBps := float64(test_data_size*num_test_updates) / totalDuration.Seconds()
	throughputOps := float64(num_test_updates) / totalDuration.Seconds()
	fmt.Printf("Duration: %v\nThroughput: %v bytes/sec (%v KB/s)\nOperations: %v ops\n",
		totalDuration, throughputBps, (throughputBps / 1024), throughputOps)
	saveTimestampFile(sendTimes, commitTimes)
}

func collectStatuses(commitChannel chan *client.Commit, commitTimes []time.Time, done chan bool) {
	for i := range len(commitTimes) {
		commitPtr := <-commitChannel
		commitStatus, err := commitPtr.Status()
		if err != nil {
			panic(fmt.Errorf("put transaction %v failed to commit with error: %w", i, err))
		}
		if !commitStatus.Successful {
			panic(fmt.Errorf("put transaction %v failed to commit, status was %+v", i, commitStatus))
		}
		commitTimes[i] = time.Now()
		slog.Debug(fmt.Sprintf("Transaction #%v finished with commit status %+v\n", i, commitStatus))
	}
	done <- true
}

func saveTimestampFile(sendTimes []time.Time, commitTimes []time.Time) {
	const TLT_READY_TO_SEND = 11000
	const TLT_EC_SIGNATURE_NOTIFY = 12002
	f, err := os.Create("timestamp.log")
	if err != nil {
		panic(fmt.Errorf("could not open timestamp.log file for writing: %w", err))
	}
	defer f.Close()
	fileWriter := bufio.NewWriter(f)
	for i := range len(sendTimes) {
		fileWriter.WriteString(fmt.Sprintf("%d %d %d\n", TLT_READY_TO_SEND, i, sendTimes[i].UnixNano()))
	}
	for i := range len(commitTimes) {
		fileWriter.WriteString(fmt.Sprintf("%d %d %d\n", TLT_EC_SIGNATURE_NOTIFY, i, commitTimes[i].UnixNano()))
	}
	fileWriter.Flush()
}

func generateWorkload(numObjects int, size int) map[string][]byte {
	workloadMap := make(map[string][]byte)
	for i := range numObjects {
		workloadMap[fmt.Sprintf("testKey_%d", i)] = generateRandomStringBytes(size)
	}
	return workloadMap
}

// Generates a random string, but stores it in a byte array instead of a string
func generateRandomStringBytes(length int) []byte {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
	stringBytes := make([]byte, length)
	for i := 0; i < length; i++ {
		stringBytes[i] = letters[rand.Intn(len(letters))]
	}
	return stringBytes
}

// Generates a random alphanumeric string
func generateRandomString(length int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
	stringBuilder := strings.Builder{}
	stringBuilder.Grow(length)
	for i := 0; i < length; i++ {
		stringBuilder.WriteByte(letters[rand.Intn(len(letters))])
	}
	return stringBuilder.String()
}

// newGrpcConnection creates a gRPC connection to the Gateway server.
func newGrpcConnection() *grpc.ClientConn {
	certificatePEM, err := os.ReadFile(tlsCertPath)
	if err != nil {
		panic(fmt.Errorf("failed to read TLS certifcate file: %w", err))
	}

	certificate, err := identity.CertificateFromPEM(certificatePEM)
	if err != nil {
		panic(err)
	}

	certPool := x509.NewCertPool()
	certPool.AddCert(certificate)
	transportCredentials := credentials.NewClientTLSFromCert(certPool, gatewayPeer)

	connection, err := grpc.NewClient(peerEndpoint, grpc.WithTransportCredentials(transportCredentials))
	if err != nil {
		panic(fmt.Errorf("failed to create gRPC connection: %w", err))
	}

	return connection
}

// newIdentity creates a client identity for this Gateway connection using an X.509 certificate.
func newIdentity() *identity.X509Identity {
	certificatePEM, err := readFirstFile(certPath)
	if err != nil {
		panic(fmt.Errorf("failed to read certificate file: %w", err))
	}

	certificate, err := identity.CertificateFromPEM(certificatePEM)
	if err != nil {
		panic(err)
	}

	id, err := identity.NewX509Identity(mspID, certificate)
	if err != nil {
		panic(err)
	}

	return id
}

// newSign creates a function that generates a digital signature from a message digest using a private key.
func newSign() identity.Sign {
	privateKeyPEM, err := readFirstFile(keyPath)
	if err != nil {
		panic(fmt.Errorf("failed to read private key file: %w", err))
	}

	privateKey, err := identity.PrivateKeyFromPEM(privateKeyPEM)
	if err != nil {
		panic(err)
	}

	sign, err := identity.NewPrivateKeySign(privateKey)
	if err != nil {
		panic(err)
	}

	return sign
}

func readFirstFile(dirPath string) ([]byte, error) {
	dir, err := os.Open(dirPath)
	if err != nil {
		return nil, err
	}

	fileNames, err := dir.Readdirnames(1)
	if err != nil {
		return nil, err
	}

	return os.ReadFile(path.Join(dirPath, fileNames[0]))
}

// Submit a transaction synchronously, blocking until it has been committed to the ledger.
func putStringData(contract *client.Contract, key string, data []byte) {
	slog.Debug(fmt.Sprintf("\n--> Submit Transaction: PutString, with key = %s and data size %d \n", key, len(data)))

	_, err := contract.Submit("PutString", client.WithBytesArguments([]byte(key), data))
	if err != nil {
		printErrorDetails(err)
		panic("Failed to submit the PutString transaction")
	}

	slog.Debug(fmt.Sprintf("*** Transaction committed successfully\n"))
}

func getStringData(contract *client.Contract, key string) {
	slog.Debug(fmt.Sprintf("\n--> Evaluate Transaction: GetString, returns data associated with current version of key %s \n", key))

	result, err := contract.EvaluateTransaction("GetString", key)
	if err != nil {
		printErrorDetails(err)
		panic("Failed to evaluate the GetString transaction")
	}

	slog.Debug(fmt.Sprintf("*** Got result: %s", result))
}

func putStringAsync(contract *client.Contract, key string, data []byte) *client.Commit {
	slog.Debug(fmt.Sprintf("Submitting async put with key %s", key))
	_, commit, err := contract.SubmitAsync("PutString", client.WithBytesArguments([]byte(key), data))
	if err != nil {
		printErrorDetails(err)
		panic("Failed to submit transaction asynchronously")
	}
	return commit
}

// Submit transaction asynchronously, blocking until the transaction has been sent to the orderer, and allowing
// this thread to process the chaincode response (e.g. update a UI) without waiting for the commit notification
func putAsync(contract *client.Contract, key string, data []byte) {
	fmt.Printf("\n--> Async Submit Transaction: PutString, with key = %s and data size %d \n", key, len(data))

	submitResult, commit, err := contract.SubmitAsync("PutString", client.WithBytesArguments([]byte(key), data))
	if err != nil {
		panic(fmt.Errorf("failed to submit transaction asynchronously: %w", err))
	}

	fmt.Printf("\n*** Successfully submitted transaction to update key with new data, returned result was %s. \n", string(submitResult))
	fmt.Println("*** Waiting for transaction commit.")

	if commitStatus, err := commit.Status(); err != nil {
		panic(fmt.Errorf("failed to get commit status: %w", err))
	} else if !commitStatus.Successful {
		panic(fmt.Errorf("transaction %s failed to commit with status: %d", commitStatus.TransactionID, int32(commitStatus.Code)))
	}

	fmt.Printf("*** Transaction committed successfully\n")
}

// Parses and unpacks the various types of errors that could result from a client.Contract method
func printErrorDetails(err error) {
	// Determine the type of error
	var endorseErr *client.EndorseError
	var submitErr *client.SubmitError
	var commitStatusErr *client.CommitStatusError
	var commitErr *client.CommitError

	if errors.As(err, &endorseErr) {
		slog.Error(fmt.Sprintf("Endorse error for transaction %s with gRPC status %v: %s\n", endorseErr.TransactionID, status.Code(endorseErr), endorseErr))
	} else if errors.As(err, &submitErr) {
		slog.Error(fmt.Sprintf("Submit error for transaction %s with gRPC status %v: %s\n", submitErr.TransactionID, status.Code(submitErr), submitErr))
	} else if errors.As(err, &commitStatusErr) {
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Error(fmt.Sprintf("Timeout waiting for transaction %s commit status: %s", commitStatusErr.TransactionID, commitStatusErr))
		} else {
			slog.Error(fmt.Sprintf("Error obtaining commit status for transaction %s with gRPC status %v: %s\n", commitStatusErr.TransactionID, status.Code(commitStatusErr), commitStatusErr))
		}
	} else if errors.As(err, &commitErr) {
		slog.Error(fmt.Sprintf("Transaction %s failed to commit with status %d: %s\n", commitErr.TransactionID, int32(commitErr.Code), err))
	} else {
		slog.Error(fmt.Sprintf("unexpected error type %T: %v", err, err))
	}

	// Any error that originates from a peer or orderer node external to the gateway will have its details
	// embedded within the gRPC status error. Extract the details so we can see what went wrong.
	statusErr := status.Convert(err)

	details := statusErr.Details()
	if len(details) > 0 {
		slog.Error(fmt.Sprintln("Error Details:"))

		for _, detail := range details {
			switch detail := detail.(type) {
			case *gateway.ErrorDetail:
				slog.Error(fmt.Sprintf("- address: %s, mspId: %s, message: %s\n", detail.Address, detail.MspId, detail.Message))
			}
		}
	}
}
