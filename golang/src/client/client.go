// Package pingtest implements a lightweight bandwidth estimation using 'ping.'
package main

import (
	"errors"
	"flag"
	"log"
	"math"
	"math/rand"
	"net"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"
	ptpb "ptprotos"
)

var (
	clientIP           = flag.String("client_ip", "127.0.0.1", "The IP address of the sending client.")
	serverIP           = flag.String("server_ip", "127.0.0.1", "The IP address of the ack server.")
	clientRcvPort      = flag.Int("client_rcv_port", 3141, "The client's port that listens for ACKs")
	clientSndPort      = flag.Int("client_snd_port", 3142, "The client's port that sends large packets.")
	serverRcvPort      = flag.Int("server_rcv_port", 3143, "The server's port that listens for packets.")
	serverSndPort      = flag.Int("server_snd_port", 3144, "The server's port that sends ACKS.")
	randomize          = flag.Bool("randomize", true, "Randomize the UDP packet padding.")
	runExpIncrease     = flag.Bool("run_exp_increase", true, "Run the exponential increase phase 1 beginning.")
	runSearchOnly      = flag.Bool("run_search_only", false, "Run binary search only.")
	increaseFactor     = flag.Int("increase_factor", 10, "Increase factor during exponential increase.")
	decreaseFactor     = flag.Int("decrease_factor", 2, "Decrease factor during binary search.")
	startSize          = flag.Int("start_size", 30, "The size of the smallest packet, in bytes.")
	maxSize            = flag.Int("max_size", 64000, "The size of the largest packet, in bytes.")
	phaseOneNumPackets = flag.Int("phase_one_num_packets", 3, "The number of packets to send during the exponential increase phase.")
	phaseTwoNumPackets = flag.Int("phase_two_num_packets", 10, "The number of packets to send during the binary search phase.")
	lossRate           = flag.Int("loss_rate", 70, "The acceptible rate of packets to receive before increasing the packet size.")
	convergeSize       = flag.Int("converge_size", 200, "When the difference is packet sizes is less than converge_size, terminate the procedure.")
)

const (
	// maxUDPSize is the maximum size, in bytes, of a single UDP packet.
	maxUDPSize = 65000
)

// Client holds parameters to run a ping test.
type Client struct {
	// The host ip to ping.
	IP             string
	Params         Params
	RunExpIncrease bool
	RunSearchOnly  bool
}

// Params holds parameters for the ping test.
type Params struct {
	// Increase the ping packet size by IncreaseFactor on each run during phase 1.
	IncreaseFactor int
	// Decrease the packet size by 1/DecreaseFactor after failing to ping during phase 2.
	DecreaseFactor int
	// Smallest packet size in bytes.
	StartSize int
	// The largest packet size to send in bytes.
	MaxSize int
	// The number of packets to send for each ping during phase 1.
	PhaseOneNumPackets int
	// The number of packets to send for each ping during phase 2.
	PhaseTwoNumPackets int
	// The minimum packet loss % that is acceptable.
	LossRate int
	// When we test packet sizes that are less than ConvergeSize bytes apart, finish the ping test.
	ConvergeSize int
}

// TestStats holds statistics for an entire speedtest.
type TestStats struct {
	// TotalBytesSent is the total bytes that were *attempted* to send.
	TotalBytesSent int
	// TotalBytesDropped is the total bytes that were dropped during this ping.
	TotalBytesDropped int
	// The estimated bandwidth.
	EstimatedMB float64
	EstimatedKb float64
}

// pingStats holds statistics for *each* ping that was sent.
// TODO(jvesuna): use median instead of mean.
type pingStats struct {
	min   float64
	max   float64
	mean  float64
	stdev float64
	loss  float64
	wg    sync.WaitGroup
}

// receivedPacket contains the full proto for each packet recieved and the sent index of the packet.
type receivedPacket struct {
	message   ptpb.PingtestMessage
	sentIndex int
}

// BySentIndex implements sort.Inteface for []receivedPacket based on the sentIndex field.
type BySentIndex []receivedPacket

func (s BySentIndex) Len() int           { return len(s) }
func (s BySentIndex) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s BySentIndex) Less(i, j int) bool { return s[i].sentIndex < s[j].sentIndex }

// checkError just logs the error.
//TODO(jvesuna): Remove this function with better error handling.
func checkError(err error) {
	if err != nil {
		log.Println("Error: ", err)
	}
}

// computeDelays returns the min, mean, and max delays of a sorted list of packets and received
// times.
func computeDelays(rcvPackets []receivedPacket, rcvTimes []int64) (float64, float64, float64, error) {
	if len(rcvPackets) != len(rcvTimes) {
		return 0, 0, 0, errors.New("error: length of rcvPackets != length of rcvTimes")
	}
	if len(rcvPackets) == 0 {
		return 0, 0, 0, nil
	}
	var delays []int64
	for i, _ := range rcvPackets {
		delays = append(delays, (rcvTimes[i]-*rcvPackets[i].message.PingtestParams.ClientTimestampNano)/1000000)
	}
	sum := 0
	min, max := delays[0], delays[0]
	for i := 1; i < len(delays); i++ {
		sum += int(delays[i])
		if delays[i] < min {
			min = delays[i]
		}
		if delays[i] > max {
			max = delays[i]
		}
	}
	mean := float64(sum) / float64(len(rcvPackets))
	return float64(min), mean, float64(max), nil
}

// clientReceiver receives ping ACKs from the server and returns ping statistics.
// timeoutLen is the duration to wait for each packet in Milliseconds.
// Note that the returned error is discarded in the goroutine.
func (ps *pingStats) clientReceiver(count, timeoutLen int) error {
	// TODO(jvesuna): Extract server into submethod w/ error checking.
	// Bind to a local port and address.
	ServerAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:"+strconv.Itoa(*clientRcvPort))
	checkError(err)
	ServerConn, err := net.ListenUDP("udp", ServerAddr)
	checkError(err)
	defer ServerConn.Close()

	var rcvCount int
	var rcvPackets []receivedPacket
	var rcvTimes []int64

	// TODO(jvesuna): Consider making everythin in the for loop a big goroutine.
	for i := 0; i < count; i++ {
		buf := make([]byte, maxUDPSize)
		ServerConn.SetReadDeadline(time.Now().Add(time.Duration(timeoutLen) * time.Millisecond))
		numBytesRead, _, err := ServerConn.ReadFromUDP(buf)
		if err != nil {
			// Handle timeout.
			if reflect.TypeOf(err) == reflect.TypeOf((*net.OpError)(nil)) {
				log.Printf("Timeout!")
			} else {
				// TODO(jvesuna): Handle.
				log.Printf("Error: ", err)
			}
		} else {
			// Packet received.
			// TODO(jvesuna): Handle in goroutine with a shared struct.
			buf = buf[:numBytesRead]
			log.Println("Received packet")
			rcvCount++
			message := &ptpb.PingtestMessage{}
			if err := proto.Unmarshal(buf, message); err != nil {
				// TODO(jvesuna): Fix this error handling.
				log.Println(err)
				log.Println("fix this, error reading proto from packet")
			}
			sentIndex := *message.PingtestParams.PacketIndex
			rcvPackets = append(rcvPackets, receivedPacket{
				message:   *message,
				sentIndex: int(sentIndex),
			})
			rcvTimes = append(rcvTimes, time.Now().UnixNano())
		}
		i++
	}

	// Process packets.
	sort.Sort(BySentIndex(rcvPackets))

	percentLoss := 100 - (float64(rcvCount)/float64(count))*100
	minDelay, meanDelay, maxDelay, err := computeDelays(rcvPackets, rcvTimes)
	if err != nil {
		// TODO(jvesuna): Fix this error handling!
		log.Println(err)
	}

	ps.min = minDelay
	ps.mean = meanDelay
	ps.max = maxDelay
	ps.loss = percentLoss
	log.Println("PingStats:", *ps)
	defer ps.wg.Done()
	return nil
}

// runPing runs a UDP implementation of the ping util.
// count is the number of UDP packets to send.
// size is in bytes.
// timeoutLen is in milliseconds.
func runPing(count, size, timeoutLen int) (pingStats, error) {
	log.Printf("========= starting new ping =========")
	log.Println("Using byte size: ", size)

	// Startup receiver.
	var ps pingStats
	ps.wg.Add(1)
	go ps.clientReceiver(count, timeoutLen)

	// Send packets

	// TODO(jvesuna): Fix port binding.
	// Bind to a local port and address.
	clientSenderAddr, err := net.ResolveUDPAddr("udp", *serverIP+":"+strconv.Itoa(*serverRcvPort))
	checkError(err)

	//TODO(jvesuna): Create the connection to send on.
	LocalAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:"+strconv.Itoa(*clientSndPort))
	checkError(err)
	Conn, err := net.DialUDP("udp", LocalAddr, clientSenderAddr)
	checkError(err)

	defer Conn.Close()
	log.Printf("Client sender beginning to send...")

	for i := 0; i < count; i++ {
		params := &ptpb.PingtestParams{
			PacketIndex:         proto.Int64(int64(i)),
			ClientTimestampNano: proto.Int64(time.Now().UnixNano()),
			PacketSizeBytes:     proto.Int64(int64(size)),
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

		wireBytes, err := proto.Marshal(message)
		checkError(err)
		// Send the packet.
		// TODO(jvesuna):  Handle host down and network problems.
		Conn.SetWriteBuffer(proto.Size(message))
		_, err = Conn.Write(wireBytes)
		checkError(err)
		time.Sleep(1 * time.Second)
	}

	ps.wg.Wait() // This goes at the end.
	return ps, nil
}

// checkDefaults ensures that Params values are valid, and if not, assigns default ones.
// TODO(jvesuna): Remove this when the caller is specified.
func checkDefaults(oldClient *Client) (Client, error) {
	if oldClient.RunExpIncrease && oldClient.RunSearchOnly {
		return Client{}, errors.New("cannot run exponential increase AND binary search only flags together")
	}
	c := Client{IP: oldClient.IP, RunExpIncrease: oldClient.RunExpIncrease, RunSearchOnly: oldClient.RunSearchOnly,
		Params: oldClient.Params}
	if c.IP == "" {
		c.IP = "216.146.46.10"
	}
	if c.Params.IncreaseFactor == 0 {
		c.Params.IncreaseFactor = 10
	}
	if c.Params.DecreaseFactor == 0 {
		c.Params.DecreaseFactor = 2
	}
	if c.Params.StartSize == 0 {
		c.Params.StartSize = 2500
	}
	if c.Params.MaxSize == 0 {
		c.Params.MaxSize = 2500
	}
	if c.Params.PhaseOneNumPackets == 0 {
		c.Params.PhaseOneNumPackets = 5
	}
	if c.Params.PhaseTwoNumPackets == 0 {
		c.Params.PhaseTwoNumPackets = 10
	}
	if c.Params.LossRate == 0 {
		c.Params.LossRate = 70
	}
	if c.Params.ConvergeSize == 0 {
		c.Params.ConvergeSize = 200
	}
	return c, nil
}

// bandwidth returns the MBps and Mbps for the given packet size and RTT.
// TODO(jvesuna): Integrate this into TestStats.
func bandwidth(pktSize int, minRTT float64) (float64, float64) {
	bytes := float64(pktSize) / ((minRTT / float64(2)) * 1000)
	return bytes, 8000 * bytes
}

// expIncrease runs pings with exponentially increasing packet sizes. Returns the smallest packet
// size that was dropped (ceiling), and TestStats. Assumes that startSize already works, thus the
// first packet we send is startSize * increaseFactor bytes.
func (c *Client) expIncrease(ts TestStats, startSize, maxSize, increaseFactor int) (int, TestStats, error) {
	pktSize := startSize
	for {
		pktSize *= increaseFactor
		if pktSize > maxSize {
			pktSize = maxSize
		}
		ts.TotalBytesSent += c.Params.PhaseOneNumPackets * pktSize
		// TODO(jvesuna): Fix timeoutLen.
		ps, err := runPing(c.Params.PhaseOneNumPackets, pktSize, 10000)
		ts.TotalBytesDropped += int(ps.loss * 0.01 * float64(c.Params.PhaseOneNumPackets) * float64(pktSize))
		if err != nil {
			log.Fatalf("failed to ping host: %v", err)
			return 0, TestStats{}, err
		}
		if ps.loss > float64(c.Params.LossRate) {
			log.Printf("Loss is over %d percent: %f", c.Params.LossRate, ps.loss)
			return pktSize, ts, nil
		}
		if pktSize >= maxSize {
			// Ping test passed. More tests need to be run to detemine a higher limit.
			// TODO(jvesuna): Clean up logging, move to caller.
			log.Printf("Passed Ping Test, hit hard limit")
			log.Printf("Total bytes attempted to send: %d", ts.TotalBytesSent)
			log.Printf("Total MB attempted to send: %f", float64(ts.TotalBytesSent)/float64(1000000))
			log.Printf("Total bytes dropped: %d", ts.TotalBytesDropped)
			log.Printf("Net megabytes that made it: %f", float64(ts.TotalBytesSent-ts.TotalBytesDropped)/float64(1000000))
			ts.EstimatedMB, ts.EstimatedKb = bandwidth(pktSize, ps.mean)
			return maxSize, ts, nil
		}
	}
}

// binarySearch returns the largest packet size that has a loss rate < c.Params.LossRate, and
// TestStats. Assume that minBytes works and maxBytes is dropped.
func (c *Client) binarySearch(ts TestStats, minBytes, maxBytes, decreaseFactor int) (int, TestStats, error) {
	log.Printf("Testing range %d to %d bytes sized packets.\n", minBytes, maxBytes)
	// middleGround is always the average of minBytes and maxBytes.
	middleGround := int(float64(maxBytes+minBytes) / float64(c.Params.DecreaseFactor))
	// oldGround was the previous middleGround, used as the new upper or lower bound in binary search.
	var oldGround int
	for {
		ts.TotalBytesSent += c.Params.PhaseTwoNumPackets * middleGround
		// TODO(jvesuna): Fix timeoutLen.
		ps, err := runPing(c.Params.PhaseTwoNumPackets, middleGround, 10000)
		ts.TotalBytesDropped += int(ps.loss * 0.01 * float64(c.Params.PhaseTwoNumPackets) * float64(middleGround))
		if err != nil {
			log.Fatalf("failed to ping host: %v", err)
			return 0, TestStats{}, err
		}
		// Update oldGround to be the previous middleGround.
		oldGround = middleGround
		// Update middleGround to either the average of (middleGround, minBytes) or the average of
		// (middleGround, maxBytes).
		if ps.loss > float64(c.Params.LossRate) {
			// Decrease middleGround. New ceiling is middleGround.
			log.Printf("Loss is over %d percent: %f", c.Params.LossRate, ps.loss)
			maxBytes = middleGround
			middleGround = int(float64(minBytes+middleGround) / float64(c.Params.DecreaseFactor))
			log.Printf("Updating range: %d to %d\n", minBytes, maxBytes)
		} else {
			// Increase middleGround. New floor is middleGround.
			minBytes = middleGround
			middleGround = int(float64(maxBytes+middleGround) / float64(c.Params.DecreaseFactor))
			log.Printf("Updating range: %d to %d\n", minBytes, maxBytes)
		}

		if math.Abs(float64(middleGround-oldGround)) < float64(c.Params.ConvergeSize) {
			// Finished! Once we have narrowed down the packet size, return.
			log.Printf("Completed Ping Test, found estimate with largest packet size: %d", middleGround)
			log.Printf("Total bytes attempted to send: %d", ts.TotalBytesSent)
			log.Printf("Total MB attempted to send: %f", float64(ts.TotalBytesSent)/float64(1000000))
			log.Printf("Total bytes dropped: %d", ts.TotalBytesDropped)
			log.Printf("Net megabytes that made it: %f", float64(ts.TotalBytesSent-ts.TotalBytesDropped)/float64(1000000))
			ts.EstimatedMB, ts.EstimatedKb = bandwidth(middleGround, ps.mean)
			return middleGround, ts, nil
		}
	}
}

// runExpIncrease first runs an exponentially increasing byte size phase (1), then a binary search
// phase (2).
func (c *Client) runExpIncrease(ts TestStats) (TestStats, error) {
	// First, find the smallest RTT.
	// TODO(jvesuna): Add test time by taking timestamp here and at return points.
	// TODO(jvesuna): Find a better way of accumulating bytes, maybe in runPing?
	ts.TotalBytesSent += c.Params.PhaseOneNumPackets * c.Params.StartSize
	// TODO(jvesuna): Fix timeoutlen.
	ps, err := runPing(c.Params.PhaseOneNumPackets, c.Params.StartSize, 10000)
	ts.TotalBytesDropped += int(ps.loss * 0.01 * float64(c.Params.PhaseOneNumPackets) * float64(c.Params.StartSize))
	if err != nil {
		log.Fatalf("failed to ping host: %v", err)
		return TestStats{}, err
	}
	// TODO(jvesuna): add some sanity checks
	log.Printf("Initial ping results: %v", ps)

	// Begin Phase 1: exponential increase.
	pktSize, ts, err := c.expIncrease(ts, c.Params.StartSize, c.Params.MaxSize, c.Params.IncreaseFactor)
	if err != nil {
		return ts, err
	}
	if pktSize == c.Params.MaxSize {
		// Pingtest passed. Need to run more tests.
		return ts, nil
	}
	if pktSize == 0 {
		return TestStats{}, errors.New("pingtest: unexpected error in exponential increase")
	}
	// End Phase 1.

	// We know maxBytes sized packets previously failed, set this as our upper bound.
	maxBytes := pktSize
	// We know minBytes sized packets worked, set this as our lower bound.
	minBytes := pktSize / c.Params.IncreaseFactor

	// Start Phase 2: binary search.
	pktSize, ts, err = c.binarySearch(ts, minBytes, maxBytes, c.Params.DecreaseFactor)
	if err != nil {
		return ts, err
	}
	if pktSize == 0 {
		return TestStats{}, errors.New("pingtest: unexpected error in binary search")
	}
	// End Phase 2.
	return ts, nil
	// End RunExpIncrease
}

// runSearchOnly only runs binary search on the given parameters.
func (c *Client) runSearchOnly(ts TestStats) (TestStats, error) {
	// First run smallest packet size.
	ts.TotalBytesSent += c.Params.PhaseOneNumPackets * c.Params.StartSize
	// TODO(jvesuna): Fix timeoutlen.
	ps, err := runPing(c.Params.PhaseOneNumPackets, c.Params.StartSize, 10000)
	ts.TotalBytesDropped += int(ps.loss * 0.01 * float64(c.Params.PhaseOneNumPackets) * float64(c.Params.StartSize))
	if err != nil {
		log.Fatalf("failed to ping host: %v", err)
		return TestStats{}, err
	}
	// Then run largest packet size.
	ts.TotalBytesSent += c.Params.PhaseOneNumPackets * c.Params.MaxSize
	// TODO(jvesuna): Fix timeoutLen.
	ps, err = runPing(c.Params.PhaseOneNumPackets, c.Params.MaxSize, 10000)
	ts.TotalBytesDropped += int(ps.loss * 0.01 * float64(c.Params.PhaseOneNumPackets) * float64(c.Params.MaxSize))
	if err != nil {
		log.Fatalf("failed to ping host: %v", err)
		return TestStats{}, err
	}
	if ps.loss < float64(c.Params.LossRate) {
		// Pingtest passed. Need to run more tests.
		log.Printf("Completed Ping Test")
		log.Printf("Total bytes *attempted* to send: %d", ts.TotalBytesSent)
		log.Printf("Total MB *attempted* to send: %f", float64(ts.TotalBytesSent)/float64(1000000))
		log.Printf("Total bytes dropped: %d", ts.TotalBytesDropped)
		log.Printf("Net megabytes that made it: %f", float64(ts.TotalBytesSent-ts.TotalBytesDropped)/float64(1000000))
		ts.EstimatedMB, ts.EstimatedKb = bandwidth(c.Params.MaxSize, ps.mean)
		return ts, nil
	}
	// Else if largest packet size fails, then run binary search.
	pktSize, ts, err := c.binarySearch(ts, c.Params.StartSize, c.Params.MaxSize, c.Params.DecreaseFactor)
	if err != nil {
		return ts, err
	}
	if pktSize == 0 {
		return TestStats{}, errors.New("pingtest: unexpected error in binary search")
	}
	return ts, nil
}

// RunPingtest runs binary search to find the largest ping packet size without a high drop rate.
// Returns the test statistics for this test.
func (c *Client) RunPingtest() (TestStats, error) {
	// First, make sure parameters are correct.
	cl, err := checkDefaults(c)
	if err != nil {
		return TestStats{}, err
	}
	// ts holds the overall statistics for this test.
	var ts TestStats

	if cl.RunExpIncrease {
		return cl.runExpIncrease(ts)
	} else if cl.RunSearchOnly {
		return cl.runSearchOnly(ts)
	}
	return TestStats{}, errors.New("pingtest failed: ended unexpectedly")
}

func main() {
	//message := &ptpb.PingtestMessage{
	//PingtestParams: &ptpb.PingtestParams{
	//PacketIndex:         proto.Int64(int64(1)),
	//ClientTimestampNano: proto.Int64(time.Now().UnixNano()),
	//PacketSizeBytes:     proto.Int64(int64(1)),
	//},
	//}
	//startSize := proto.Size(message)

	c := Client{
		IP: *serverIP,
		Params: Params{
			IncreaseFactor:     *increaseFactor,
			DecreaseFactor:     *decreaseFactor,
			StartSize:          *startSize,
			MaxSize:            *maxSize,
			PhaseOneNumPackets: *phaseOneNumPackets,
			PhaseTwoNumPackets: *phaseTwoNumPackets,
			LossRate:           *lossRate,
			ConvergeSize:       *convergeSize,
		},
		RunExpIncrease: true,
	}
	ts, err := c.RunPingtest()
	if err != nil {
		log.Println(err)
	}
	log.Printf("Estimated bandwidth: %f MBps, %f Kbps", ts.EstimatedMB, ts.EstimatedKb)
}
