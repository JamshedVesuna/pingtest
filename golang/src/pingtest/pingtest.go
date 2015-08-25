// main ...
// TODO(jvesuna): Add usage with example input flags.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"
	ptpb "ptprotos"
)

var (
	isClient           = flag.Bool("client", false, "Set to true for client.")
	isServer           = flag.Bool("server", false, "Set to true for server.")
	clientIP           = flag.String("client_ip", "127.0.0.1", "The IP address of the sending client.")
	serverIP           = flag.String("server_ip", "127.0.0.1", "The IP address of the ack server.")
	clientRcvPort      = flag.Int("client_rcv_port", 3141, "The client's port that listens for ACKs")
	clientSndPort      = flag.Int("client_snd_port", 3142, "The client's port that sends large packets.")
	serverRcvPort      = flag.Int("server_rcv_port", 3143, "The server's port that listens for packets.")
	serverSndPort      = flag.Int("server_snd_port", 3144, "The server's port that sends ACKS.")
	randomize          = flag.Bool("randomize", true, "Randomize the UDP packet padding.")
	runExpIncrease     = flag.Bool("run_exp_increase", true, "Run the exponential increase phase 1 beginning.")
	runSearchOnly      = flag.Bool("run_search_only", false, "Run binary search only.")
	increaseFactor     = flag.Float64("increase_factor", 10, "Increase factor during exponential increase.")
	decreaseFactor     = flag.Float64("decrease_factor", 2, "Decrease factor during binary search.")
	startSize          = flag.Int("start_size", 30, "The size of the smallest packet, in bytes.")
	maxSize            = flag.Int("max_size", 64000, "The size of the largest packet, in bytes.")
	phaseOneNumPackets = flag.Int("phase_one_num_packets", 5, "The number of packets to send during the exponential increase phase.")
	phaseTwoNumPackets = flag.Int("phase_two_num_packets", 10, "The number of packets to send during the binary search phase.")
	lossRate           = flag.Int("loss_rate", 70, "The acceptible rate of packets to receive before increasing the packet size.")
	convergeSize       = flag.Int("converge_size", 200, "When the difference is packet sizes is less than converge_size, terminate the procedure.")
)

const (
	// maxUDPSize is the maximum size, in bytes, of a single UDP packet.
	maxUDPSize = 65000
	// paramsMaxSize is the maximum size, in bytes, of PingtestParams.
	paramsMaxSize = 250
)

type SenderServer struct {
	udpAddr net.UDPAddr
	udpPort int
}

type ReceiverServer struct {
	udpPort int
}

func NewReceiverServer(hostPort int) (*ReceiverServer, error) {
	return &ReceiverServer{
		udpPort: hostPort,
	}, nil
}

func NewSenderServer(hostIP string, hostPort int) (*SenderServer, error) {
	// TODO(jvesuna): error checking.
	addr := net.UDPAddr{net.IP(net.ParseIP(hostIP)), hostPort, ""}
	return &SenderServer{
		udpAddr: addr,
		udpPort: hostPort,
	}, nil

}

// checkError just logs the error.
//TODO(jvesuna): Remove this function with better error handling.
func checkError(err error) {
	if err != nil {
		log.Printf("Error: ", err)
	}
}

// Queue holds a uniform packetSize, the number of packets to send, count, a lock, and a wait group.
// packetSize is the size of each packet, in bytes, to send.
// count is the number of packets to send.
// lock is a Mutex to be shared between goroutines.
// wg is a Wait Group that ensures all threads complete.
type Queue struct {
	packetSize int
	count      int
	lock       sync.Mutex
	wg         sync.WaitGroup // TODO(jvesuna): This wait group may not be necessary.
}

/////////////////////////////// CLIENT ///////////////////////////////

// clientReceiver receives ACKs from the server and fills the queue with the appropriate packet
// size. For each packet received, either add 1 to the q.count, or terminate.
// TODO(jvesuna): add timeout that adds to queue in case we don't receive a packet.
//TODO(jvesuna): Make TCP receiver.
func (q *Queue) clientReceiver() {
	// TODO(jvesuna): Extract server into submethod w/ error checking.
	// Bind to a local port and address.
	ServerAddr, err := net.ResolveUDPAddr("udp", ":"+strconv.Itoa(*clientRcvPort))
	checkError(err)

	// Create the connection to listen on.
	ServerConn, err := net.ListenUDP("udp", ServerAddr)
	checkError(err)
	defer ServerConn.Close()

	//totalPackets := *phaseTwoNumPackets
	//if *runExpIncrease {
	//totalPackets = *phaseOneNumPackets
	//}
	var rcvCount int
	// TODO(jvesuna): Change phaseMax based on expIncrease vs binarySearch.
	phaseMaxSeconds := 5

	// TODO(jvesuna): Add timeout.
	// Continuously listen for a packet.
	for {
		buf := make([]byte, paramsMaxSize)
		// Set timeout in seconds.
		ServerConn.SetReadDeadline(time.Now().Add(time.Duration(phaseMaxSeconds) * time.Second))
		_, addr, err := ServerConn.ReadFromUDP(buf)
		// TODO(jvesuna): Clean up error handling.
		if err != nil {
			// Timeout.
			if reflect.TypeOf(err) == reflect.TypeOf((*net.OpError)(nil)) {
				log.Printf("Timeout!")
			} else {
				// TODO(jvesuna): Handle.
				log.Printf("Error: ", err)
			}
		}

		message := &ptpb.PingtestMessage{}
		if err := proto.Unmarshal(buf, message); err != nil {
			// TODO(jvesuna): Fix this error handling.
			//log.Println("Received malformed packet:", err)
			log.Println("fix this")
		}

		rcvCount++
		if rcvCount > phaseMaxSeconds {
			// Add new packet to

			rcvCount = 0
		}

		log.Println("Received ", message)
		ackReceived := message.PingtestParams

		// TODO(jvesuna): Clean up logging.
		fmt.Println(">>> Received ", ackReceived, " from ", addr)
		q.lock.Lock()
		// TODO(jvesuna): Use pingtest algorithm to update packet size and count.
		// TODO(jvesuna): Defer until threshold packets received, or timeout.
		q.packetSize++
		q.count++
		q.lock.Unlock()
		defer q.wg.Done() // TODO(jvesuna): Is this necessary?
	}
}

// clientSender sends q.count packets to the server, each of size q.packetSize.
func (q *Queue) clientSender() {
	// Bind to a local port and address.
	clientSenderAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:"+strconv.Itoa(*serverRcvPort))
	checkError(err)

	// TODO(jvesuna): Fix port binding.
	//TODO(jvesuna): Create the connection to send on.
	LocalAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:"+strconv.Itoa(*clientSndPort))
	checkError(err)

	Conn, err := net.DialUDP("udp", LocalAddr, clientSenderAddr)
	//TODO(jvesuna): Fix error handling.
	checkError(err)

	defer Conn.Close()
	var size int
	var i int64
	// Start sending packets.
	fmt.Println("Starting to send..")
	var count int
	for {
		count++
		//TODO(jvesuna): Better way to block? Perhaps a channel or signal?
		// TODO(jvesuna): Use conditional variable.
		for q.count < 1 {
			// Block
		}
		q.lock.Lock()
		size = q.packetSize
		q.count--
		q.lock.Unlock()
		defer q.wg.Done() // TODO(jvesuna): Is this necessary?
		//TODO(jvesuna): REMOVE THIS.
		size = 100 + count
		log.Printf("Client sent packet size %d, index: %d\n", size, i)
		// Generate UDP payload with proto.
		params := &ptpb.PingtestParams{
			PacketIndex:     proto.Int64(i),
			PacketSizeBytes: proto.Int64(int64(size)),
		}

		// paddingLen is the number of bytes we should add to the payload.
		paddingLen := int(math.Max(0, float64(size-proto.Size(params))))
		var padding []byte

		// Randomize the padding
		if *randomize {
			for i := 0; i < paddingLen; i++ {
				padding = append(padding, byte(rand.Intn(10)))
			}
		} else {
			padding = make([]byte, paddingLen)
		}

		message := &ptpb.PingtestMessage{
			PingtestParams: params,
			Padding:        padding,
		}

		//fmt.Println("Sending:", message)
		wireBytes, err := proto.Marshal(message)
		checkError(err)

		// Send the packet.
		_, err = Conn.Write(wireBytes)
		checkError(err)
		i++

		//TODO(jvesuna): REMOVE THIS.
		time.Sleep(time.Second * 1)
	}
}

/////////////////////////////// MISC ///////////////////////////////

// invalidParams checks the input parameters and returns an error.
func invalidParams() error {
	// Must run as EITHER client or server.
	if *isClient && *isServer {
		return errors.New("Cannot run as client *and* server.")
	}
	if !*isClient && !*isServer {
		return errors.New("Must run as either client or server.")
	}
	// All ports must be unique.
	ports := make(map[int]int)
	for _, port := range []int{*clientRcvPort, *clientSndPort, *serverRcvPort, *serverSndPort} {
		if _, portExists := ports[port]; portExists {
			// TODO(jvesuna): Confirm that all ports must be unique.
			return errors.New("All ports must be unique")
		}
		ports[port] = 1
	}
	return nil
}

func main() {
	flag.Parse()
	if err := invalidParams(); err != nil {
		log.Println("Error:", err)
		return
	}
	if *isClient {
		fmt.Println("Running as client")
		// Create a shared Queue.
		var q Queue
		// Using 2 go routines, one sender and one receiver.
		q.wg.Add(2)
		// TODO(jvesuna): initialize queue, break out into helper.
		// TODO(jvesuna): fix initialization.
		q.packetSize = 0
		q.count = 10

		// Start receiving ACKs from the server and fill the queue.
		// TODO(jvesuna): Consider running sender as goroutine and receives natively.
		go q.clientReceiver()

		// Start sending packets to the ACK server.
		q.clientSender()
		// TODO(jvesuna): Wait for all threads to complete. We may only need to wait for receiver to
		// complete.
		q.wg.Wait() // TODO(jvesuna): Is this necessary?
	}
}
