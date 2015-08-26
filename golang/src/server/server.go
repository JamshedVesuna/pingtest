// main ...
// TODO(jvesuna): Add usage with example input flags.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"

	"github.com/golang/protobuf/proto"
	ptpb "ptprotos"
)

var (
	clientIP      = flag.String("client_ip", "127.0.0.1", "The IP address of the sending client.")
	serverIP      = flag.String("server_ip", "127.0.0.1", "The IP address of the ack server.")
	clientRcvPort = flag.Int("client_rcv_port", 3141, "The client's port that listens for ACKs")
	clientSndPort = flag.Int("client_snd_port", 3142, "The client's port that sends large packets.")
	serverRcvPort = flag.Int("server_rcv_port", 3143, "The server's port that listens for packets.")
	serverSndPort = flag.Int("server_snd_port", 3144, "The server's port that sends ACKS.")
	randomize     = flag.Bool("randomize", true, "Randomize the UDP packet padding.")
)

// checkError just logs the error.
//TODO(jvesuna): Remove this function with better error handling.
func checkError(err error) {
	if err != nil {
		log.Printf("Error: ", err)
	}
}

// invalidParams checks the input parameters and returns an error.
func invalidParams() error {
	// Must run as EITHER client or server.
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

/////////////////////////////// SERVER ///////////////////////////////

// ackServer simply sends a small ACK packet back to the sender for each packet is receives.
//TODO(jvesuna): Send TCP packets.
func ackServer() {

	var serverWG sync.WaitGroup

	ServerAddr, err := net.ResolveUDPAddr("udp", ":"+strconv.Itoa(*serverRcvPort))
	checkError(err)

	ServerConn, err := net.ListenUDP("udp", ServerAddr)
	checkError(err)
	defer ServerConn.Close()

	for {
		// TODO(jvesuna): This buff should be > 65,000 bytes.
		buf := make([]byte, 65000)
		numBytesRead, _, err := ServerConn.ReadFromUDP(buf)
		log.Println("Got packet")
		if err != nil {
			log.Println("Error: ", err)
		}
		// ACKS do not need to be ordered, so we do not need to lock.
		// TODO(jvesuna): Ensure goroutine closes.
		serverWG.Add(1)
		// TODO(jvesuna): Make this a goroutine.
		parseAndAck(buf[:numBytesRead])
	}
	serverWG.Wait()
}

// parseAndAck parses the payload and sends a tiny ACK to the Client Receiver.
// TODO(jvesuna): Fix input to be a packet struct.
// TODO(jvesuna): Keep parseAndAck routine live with a connection. Have the ackServer channel data
// to this routine for faster processing.
// TODO(jvesuna): Fix error handling.
func parseAndAck(buf []byte) {
	message := &ptpb.PingtestMessage{}
	if err := proto.Unmarshal(buf, message); err != nil {
		//TODO(jvesuna): Fix this error handling.
		log.Println("FIX: Failed to unmarshal in parseAndAck:", err)
	}

	// TODO(jvesuna): Uncomment the following 3 blocks to send tiny ACKS.
	//log.Println("Received ", message)
	//paramsReceived := message.PingtestParams

	//messageAck := &ptpb.PingtestMessage{
	//PingtestParams: paramsReceived,
	//}

	// wireBytesAck should always be about the same size. For now, we are testing sending the full
	// payload back.
	//wireBytesAck, err := proto.Marshal(messageAck)
	wireBytesAck, err := proto.Marshal(message)
	checkError(err)

	// Prepare to send ACK
	serverSenderAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:"+strconv.Itoa(*clientRcvPort))
	checkError(err)
	// TODO(jvesuna): Fix port binding.
	LocalAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:"+strconv.Itoa(*serverSndPort))
	checkError(err)
	Conn, err := net.DialUDP("udp", LocalAddr, serverSenderAddr)
	checkError(err)
	defer Conn.Close()

	// Send ACK.

	Conn.SetWriteBuffer(proto.Size(message))
	_, err = Conn.Write(wireBytesAck)
	if err != nil {
		//TODO(jvesuna): Fix.
		log.Fatal("Failed to send ACK:", err)
	}
	log.Println("Sent Ack")
}

func main() {
	flag.Parse()
	if err := invalidParams(); err != nil {
		log.Println("Error:", err)
		return
	}
	// ACK server simply ACKS each packet it receives.
	fmt.Println("Running as server")
	ackServer()
}
