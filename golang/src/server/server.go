// Package server implements a simple UDP server that ACKs every valid packet it receives.
//
// Example usage from pingtest/bin/
//  # Runs a basic server *locally* that returns each valid UDP packet received.
//	./server
//
//	# Assert ports to receive and send on and to.
//	./server --client_ip=192.168.2.3 --client_rcv_port=1234 --server_rcv_port=2000 --server_snd_port=2001
//
//	# ACKs will not contain the padding and thus be consistently small.:w
//	./server --tiny_ack=true
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
	clientRcvPort = flag.Int("client_rcv_port", 3141, "The client's port that listens for ACKs")
	serverRcvPort = flag.Int("server_rcv_port", 3143, "The server's port that listens for packets.")
	serverSndPort = flag.Int("server_snd_port", 3144, "The server's port that sends ACKs.")
	tinyAck       = flag.Bool("tiny_ack", false, "The server sends small, fixed sized ACKs instead of the entire payload.")
)

const (
	// maxUDPSize is the maximum size, in bytes, of a single UDP packet.
	maxUDPSize = 65000
)

// invalidParams checks the input parameters and returns an error.
func invalidParams() error {
	// All ports must be unique.
	ports := make(map[int]int)
	for _, port := range []int{*clientRcvPort, *serverRcvPort, *serverSndPort} {
		if _, portExists := ports[port]; portExists {
			return errors.New("All ports must be unique")
		}
		ports[port] = 1
	}
	return nil
}

// ackServer simply sends an ACK packet back to the sender for each valid UDP packet is receives.
// TODO(jvesuna): Send TCP packets instead of UDP for reliability.
func ackServer() {
	// Bind to local port.
	ServerAddr, err := net.ResolveUDPAddr("udp", ":"+strconv.Itoa(*serverRcvPort))
	if err != nil {
		log.Fatalln("error binding to local port:", err)
	}
	ServerConn, err := net.ListenUDP("udp", ServerAddr)
	if err != nil {
		log.Fatalln("error listening for UDP:", err)
	}
	defer ServerConn.Close()

	// Wait for all threads to complete before exiting.
	var serverWG sync.WaitGroup
	for {
		buf := make([]byte, maxUDPSize)
		numBytesRead, _, err := ServerConn.ReadFromUDP(buf)
		if err != nil {
			log.Println("error receiving packet: ", err)
		}
		serverWG.Add(1)
		go parseAndAck(buf[:numBytesRead])
	}
	serverWG.Wait()
}

// parseAndAck parses the UDP payload and sends anwACK to the client receiver.
// TODO(jvesuna): Keep parseAndAck routine live with a connection. Have the ackServer channel data
// to this routine for faster processing.
func parseAndAck(buf []byte) {
	messageAck := &ptpb.PingtestMessage{}
	if err := proto.Unmarshal(buf, messageAck); err != nil {
		//TODO(jvesuna): Fix this error handling.
		log.Println("error: failed to unmarshal packet in parseAndAck:", err)
	}

	if *tinyAck {
		// Don't include padding.
		// Here, messageAck should always be about the same size.
		messageAck = &ptpb.PingtestMessage{
			PingtestParams: messageAck.PingtestParams,
			Type:           messageAck.Type,
		}
	}

	wireBytesAck, err := proto.Marshal(messageAck)
	if err != nil {
		fmt.Println("error marshalling ACK:", err)
	}

	// Bind all addresses and ports.
	// TODO(jvesuna): Fix addressing, use flags.
	serverSenderAddr, err := net.ResolveUDPAddr("udp", *clientIP+":"+strconv.Itoa(*clientRcvPort))
	if err != nil {
		log.Fatalln("error binding to client address and port:", err)
	}
	// TODO(jvesuna): Fix port binding.
	LocalAddr, err := net.ResolveUDPAddr("udp", ":"+strconv.Itoa(*serverSndPort))
	if err != nil {
		log.Fatalln("error binding to local port:", err)
	}
	Conn, err := net.DialUDP("udp", LocalAddr, serverSenderAddr)
	if err != nil {
		log.Fatalln("error making connection to client:", err)
	}
	defer Conn.Close()

	// Send ACK.
	Conn.SetWriteBuffer(proto.Size(messageAck))
	_, err = Conn.Write(wireBytesAck)
	if err != nil {
		log.Println("Failed to send ACK:", err)
	}
	log.Println("Sent Ack")
}

func main() {
	flag.Parse()
	if err := invalidParams(); err != nil {
		log.Fatalln("Error:", err)
	}
	fmt.Println("Running UDP ACK server")
	ackServer()
}
