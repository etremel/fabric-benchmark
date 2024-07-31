package main

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
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
	mspID                = "Org1MSP"
	orgCryptoPath        = "peerOrganizations/org1.example.com"
	userCertsSuffix      = "users/User1@org1.example.com/msp/signcerts"
	userKeysSuffix       = "users/User1@org1.example.com/msp/keystore"
	defaultChaincodeName = "plain_string"
	defaultChannelName   = "mychannel"
	defaultGatewayPeer   = "peer0.org1.example.com"
	workloadSize         = 1024
	timestampFileName    = "timestamp.log"
	passiveClientPort    = "33333"
)

type TestClient struct {
	userCertPath     string
	userKeyPath      string
	peerTlsCertPath  string
	peerName         string
	peerEndpoint     string
	workloadObjects  map[string][]byte
	sendTimes        []time.Time
	commitTimes      []time.Time
	clientConnection *grpc.ClientConn
	gateway          *client.Gateway
	contract         *client.Contract
}

type PassiveStartMessage struct {
	TestType     string
	DataSize     int
	MessageRate  int
	TestDuration int
	NumMessages  int
	StartTime    time.Time
}

func main() {
	verbose := flag.Bool("v", false, "Verbose mode")
	testType := flag.String("testType", "throughput", "Type of test to run (throughput or latency)")
	// The default value for cryptoDir is FABRIC_CFG_PATH/crypto-config,
	// or ~/fabric-samples/config if FABRIC_CFG_PATH is not set
	fabricConfDir := os.Getenv("FABRIC_CFG_PATH")
	if fabricConfDir == "" {
		home, _ := os.UserHomeDir()
		fabricConfDir = path.Join(home, "fabric-samples", "config")
	}
	cryptoDir := flag.String("c", path.Join(fabricConfDir, "crypto-config"), "Root directory of the certificates directory")
	chaincodeName := flag.String("chaincode", defaultChaincodeName, "Name of the chaincode to invoke transactions on for testing")
	channelName := flag.String("channel", defaultChannelName, "Name of the channel the test organizations have joined")
	activeClientIP := flag.String("passive", "", "Sets this test client to passive mode; argument is the IP of the active client that will start the test")
	gatewayPeerName := flag.String("peerName", defaultGatewayPeer, "(Host) name of the peer to connect to as the gateway")
	flag.Parse()
	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	} else {
		slog.SetLogLoggerLevel(slog.LevelInfo)
	}
	var isActiveClient bool
	if *activeClientIP == "" {
		isActiveClient = true
	} else {
		isActiveClient = false
	}
	if !(strings.EqualFold(*testType, "throughput") || strings.EqualFold(*testType, "latency")) {
		fmt.Println("Unknown test type: " + *testType)
		return
	}
	if isActiveClient && strings.EqualFold(*testType, "throughput") && len(flag.Args()) < 3 {
		fmt.Println("Error: Missing required arguments")
		fmt.Printf("Usage: %s --testType throughput <data size> <num messages> <peer IP> [<client IPs>]\n", os.Args[0])
		return
	}
	if isActiveClient && strings.EqualFold(*testType, "latency") && len(flag.Args()) < 4 {
		fmt.Println("Error: Missing required arguments")
		fmt.Printf("Usage: %s --testType latency <data size> <message rate> <test duration> <peer IP> [<client IPs>]\n", os.Args[0])
		return
	}
	// A passive client only needs the peer IP argument; it will receive the test parameters from the leader (active) client
	if !isActiveClient && len(flag.Args()) < 1 {
		fmt.Println(("Error: Missing requrired argument"))
		fmt.Printf("Usage: %s --passive <leader IP> <peer IP>", os.Args[0])
		return
	}
	var numTestUpdates int
	var messageRate int
	var testDataSize int
	var testDurationSeconds int
	var peerIP string
	var passiveClientIPs []string
	if !isActiveClient {
		peerIP = flag.Arg(0)
	} else {
		// Parse the other positional arguments only for the active client
		dataSize_i64, parseErr := strconv.ParseInt(flag.Arg(0), 0, 0)
		if parseErr != nil {
			panic(fmt.Errorf("failed to parse argument 1 as an int: %w", parseErr))
		}
		// Annoyingly, ParseInt always returns an int64, which must be cast to the correct type
		testDataSize = int(dataSize_i64)
		// Parse positional arguments depending on which type of test is requested
		arg2_i64, parseErr := strconv.ParseInt(flag.Arg(1), 0, 0)
		if parseErr != nil {
			panic(fmt.Errorf("failed to parse argument 2 as an int: %w", parseErr))
		}
		if strings.EqualFold(*testType, "throughput") {
			numTestUpdates = int(arg2_i64)
			peerIP = flag.Arg(2)
			passiveClientIPs = flag.Args()[3:]
		} else if strings.EqualFold(*testType, "latency") {
			messageRate = int(arg2_i64)
			duration_i64, parseErr := strconv.ParseInt(flag.Arg(2), 0, 0)
			if parseErr != nil {
				panic(fmt.Errorf("failed to parse argument 3 as an int: %w", parseErr))
			}
			testDurationSeconds = int(duration_i64)
			peerIP = flag.Arg(3)
			passiveClientIPs = flag.Args()[4:]
		}
	}

	// Use the peer's hostname to build the path to its TLS certificate: certificates for each possible gateway peer are stored in peers/<hostname>/tls/ca.crt
	testClient := TestClient{
		userCertPath:    path.Join(*cryptoDir, orgCryptoPath, userCertsSuffix),
		userKeyPath:     path.Join(*cryptoDir, orgCryptoPath, userKeysSuffix),
		peerName:        *gatewayPeerName,
		peerTlsCertPath: path.Join(*cryptoDir, orgCryptoPath, "peers", *gatewayPeerName, "tls", "ca.crt"),
		peerEndpoint:    "dns:///" + peerIP + ":7051"}
	defer testClient.Close()
	// Initialize the GRPC connection and the client's identity functions
	testClient.newGrpcConnection()
	id := testClient.newIdentity()
	sign := testClient.newSign()

	// Create a Gateway connection for a specific client identity
	gw, err := client.Connect(
		id,
		client.WithSign(sign),
		client.WithClientConnection(testClient.clientConnection),
		// Default timeouts for different gRPC calls
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(1*time.Minute),
	)
	if err != nil {
		panic(err)
	}
	testClient.gateway = gw
	// Initialize the client Contract object for the test's channel and chaincode
	testClient.contract = testClient.gateway.GetNetwork(*channelName).GetContract(*chaincodeName)
	// If this is the active client, decide on a start time and send launch messages to the passive clients
	// If this is a passive client, start listening for a message from the active client
	if isActiveClient {
		testClient.workloadObjects = generateWorkload(workloadSize, testDataSize)
		passiveClientWriters := make([]*json.Encoder, 0, len(passiveClientIPs))
		for _, ip := range passiveClientIPs {
			passiveConn, error := net.Dial("tcp", net.JoinHostPort(ip, passiveClientPort))
			if error != nil {
				panic(fmt.Errorf("failed to connect to passive client at %v due to error %w", ip, error))
			}
			passiveClientWriters = append(passiveClientWriters, json.NewEncoder(passiveConn))
			defer passiveConn.Close()
		}
		startMessage := PassiveStartMessage{
			TestType:     *testType,
			DataSize:     testDataSize,
			MessageRate:  messageRate,
			NumMessages:  numTestUpdates,
			TestDuration: testDurationSeconds,
			StartTime:    time.Now().Add(time.Duration(5) * time.Second)}
		slog.Debug(fmt.Sprintf("Sending message to passive clients %v: %+v", passiveClientIPs, startMessage))
		for i, writer := range passiveClientWriters {
			err := writer.Encode(startMessage)
			if err != nil {
				panic(fmt.Errorf("failed to send the JSON message to client %v due to error %w", i, err))
			}
		}
		slog.Debug("Waiting until start time")
		time.Sleep(time.Until(startMessage.StartTime))
		slog.Debug("Starting test on active client")
		if strings.EqualFold(*testType, "throughput") {
			testClient.ThroughputTest(numTestUpdates)
		} else if strings.EqualFold(*testType, "latency") {
			testClient.LatencyTest(messageRate, time.Duration(testDurationSeconds)*time.Second)
		}
	} else {
		listener, err := net.Listen("tcp", ":"+passiveClientPort)
		if err != nil {
			panic(fmt.Errorf("passive client failed to listen on port %v: %w", passiveClientPort, err))
		}
		defer listener.Close()
		activeConn, err := listener.Accept()
		if err != nil {
			panic(fmt.Errorf("passive client failed to accept a connection due to error: %w", err))
		}
		defer activeConn.Close()
		jsonReader := json.NewDecoder(activeConn)
		var startMessage PassiveStartMessage
		err = jsonReader.Decode(&startMessage)
		if err != nil {
			panic(fmt.Errorf("passive client failed to read a JSON message from the socket, error was: %w", err))
		}
		testClient.workloadObjects = generateWorkload(workloadSize, startMessage.DataSize)
		slog.Debug(fmt.Sprintf("Passive client got a start message: %v. Waiting until the start time", startMessage))
		time.Sleep(time.Until(startMessage.StartTime))
		slog.Debug("Starting test on passive client")
		if strings.EqualFold(startMessage.TestType, "throughput") {
			testClient.ThroughputTest(startMessage.NumMessages)
		} else if strings.EqualFold(*testType, "latency") {
			testClient.LatencyTest(startMessage.MessageRate, time.Duration(startMessage.TestDuration)*time.Second)
		}
	}

}

func (tc *TestClient) ThroughputTest(numTestUpdates int) {
	putCommitChannel := make(chan *client.Commit, numTestUpdates)
	doneChannel := make(chan bool)
	tc.sendTimes = make([]time.Time, numTestUpdates)
	tc.commitTimes = make([]time.Time, numTestUpdates)
	// Start a thread to receive commit pointers from the SubmitAsync calls and wait on them
	go collectStatusesFixed(putCommitChannel, tc.commitTimes, doneChannel)
	// Send a bunch of put updates, each targeting a random key, with the test data
	// Measure the total time taken and divide by the total number of bytes sent to get the throughput
	beginTime := time.Now()
	for i := range numTestUpdates {
		testObjectKey := fmt.Sprintf("testKey_%d", rand.Intn(len(tc.workloadObjects)))
		tc.sendTimes[i] = time.Now()
		putCommitChannel <- putStringAsync(tc.contract, testObjectKey, tc.workloadObjects[testObjectKey])
	}
	// Wait for the collectStatuses thread to finish
	<-doneChannel
	endTime := time.Now()
	totalDuration := endTime.Sub(beginTime)
	throughputBps := float64(len(tc.workloadObjects["testKey_0"])*numTestUpdates) / totalDuration.Seconds()
	throughputOps := float64(numTestUpdates) / totalDuration.Seconds()
	fmt.Printf("Duration: %v\nThroughput: %v bytes/sec (%v KB/s)\nOperations: %v ops\n",
		totalDuration, throughputBps, (throughputBps / 1024), throughputOps)
	tc.saveTimestampFile()
}

func (tc *TestClient) LatencyTest(messagesPerSecond int, testDuration time.Duration) {
	putCommitChannel := make(chan *client.Commit, messagesPerSecond*int(testDuration.Seconds()))
	doneChannel := make(chan bool)
	lastMessageChannel := make(chan int)
	tc.sendTimes = make([]time.Time, 0, messagesPerSecond*int(testDuration.Seconds()))
	tc.commitTimes = make([]time.Time, 0, messagesPerSecond*int(testDuration.Seconds()))
	loopInterval := time.Second / time.Duration(messagesPerSecond)
	// Debugging: Check my time math
	slog.Debug(fmt.Sprintf("Test duration %v, sending %v messages per second, so loop interval is %v", testDuration, messagesPerSecond, loopInterval))
	// Start a thread to receive commit pointers from the SubmitAsync calls and wait on them
	go collectStatusesFlexible(putCommitChannel, tc.commitTimes, lastMessageChannel, doneChannel)
	messageCounter := 0
	// Loop for the requested test duration
	endTime := time.Now().Add(testDuration)
	lastSentTime := time.Now()
	for time.Now().Before(endTime) {
		testObjectKey := fmt.Sprintf("testKey_%d", rand.Intn(len(tc.workloadObjects)))
		waitTime := time.Until(lastSentTime.Add(loopInterval))
		if waitTime < 0 {
			slog.Warn("Send interval time has already elapsed, sending slower than the requested rate!")
		}
		time.Sleep(waitTime)
		lastSentTime = time.Now()
		tc.sendTimes = append(tc.sendTimes, lastSentTime)
		putCommitChannel <- putStringAsync(tc.contract, testObjectKey, tc.workloadObjects[testObjectKey])
		messageCounter++
	}
	slog.Debug(fmt.Sprintf("All messages sent, last counter value was %v", messageCounter-1))
	// Tell the collect-statuses thread what the last message number is
	lastMessageChannel <- messageCounter - 1
	// Wait for it to be done
	slog.Debug("Waiting for collect-statuses thread to finish")
	<-doneChannel
	tc.saveTimestampFile()
}

func collectStatusesFixed(commitChannel <-chan *client.Commit, commitTimes []time.Time, done chan<- bool) {
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

func collectStatusesFlexible(commitChannel <-chan *client.Commit, commitTimes []time.Time,
	lastMessageChannel <-chan int, done chan<- bool) {
	messageCounter := 0
	var lastMessageNum int
	lastMessageNumReceived := false
	for {
		commitPtr := <-commitChannel
		commitStatus, err := commitPtr.Status()
		if err != nil {
			panic(fmt.Errorf("put transaction %v failed to commit with error: %w", messageCounter, err))
		}
		if !commitStatus.Successful {
			panic(fmt.Errorf("put transaction %v failed to commit, status was %+v", messageCounter, commitStatus))
		}
		commitTimes = append(commitTimes, time.Now())
		slog.Debug(fmt.Sprintf("Transaction #%v finished with commit status %+v\n", messageCounter, commitStatus))
		select {
		case lastMessageNum = <-lastMessageChannel:
			slog.Debug(fmt.Sprintf("Last message number received, it is %v", lastMessageNum))
			lastMessageNumReceived = true
		default:
			// continue
		}
		if lastMessageNumReceived && messageCounter == lastMessageNum {
			break
		}
		messageCounter++
	}
	done <- true
}

func (tc *TestClient) saveTimestampFile() {
	const TLT_READY_TO_SEND = 11000
	const TLT_EC_SIGNATURE_NOTIFY = 12002
	f, err := os.Create(timestampFileName)
	if err != nil {
		panic(fmt.Errorf("could not open %v file for writing: %w", timestampFileName, err))
	}
	defer f.Close()
	fileWriter := bufio.NewWriter(f)
	for i := range len(tc.sendTimes) {
		fileWriter.WriteString(fmt.Sprintf("%d %d %d\n", TLT_READY_TO_SEND, i, tc.sendTimes[i].UnixNano()))
	}
	for i := range len(tc.commitTimes) {
		fileWriter.WriteString(fmt.Sprintf("%d %d %d\n", TLT_EC_SIGNATURE_NOTIFY, i, tc.commitTimes[i].UnixNano()))
	}
	fileWriter.Flush()
}

func (tc *TestClient) Close() {
	tc.clientConnection.Close()
	tc.gateway.Close()
}

// creates a gRPC connection to the Gateway server
func (tc *TestClient) newGrpcConnection() {
	certificatePEM, err := os.ReadFile(tc.peerTlsCertPath)
	if err != nil {
		panic(fmt.Errorf("failed to read TLS certificate file: %w", err))
	}

	certificate, err := identity.CertificateFromPEM(certificatePEM)
	if err != nil {
		panic(err)
	}

	certPool := x509.NewCertPool()
	certPool.AddCert(certificate)
	transportCredentials := credentials.NewClientTLSFromCert(certPool, tc.peerName)

	connection, err := grpc.NewClient(tc.peerEndpoint, grpc.WithTransportCredentials(transportCredentials))
	if err != nil {
		panic(fmt.Errorf("failed to create gRPC connection: %w", err))
	}

	tc.clientConnection = connection
}

// newIdentity creates a client identity for this Gateway connection using an X.509 certificate.
func (tc *TestClient) newIdentity() *identity.X509Identity {
	certificatePEM, err := readFirstFile(tc.userCertPath)
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
func (tc *TestClient) newSign() identity.Sign {
	privateKeyPEM, err := readFirstFile(tc.userKeyPath)
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

// Generates a collection of random key-value pairs, where the keys are all in the format testKey_N
// and the values are all strings (as byte arrays) of the same fixed size
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
