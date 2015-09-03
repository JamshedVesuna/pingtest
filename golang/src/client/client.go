// Package client implements a lightweight bandwidth estimation using a modified UDP
// implementation of ping.
//
// Example usage from golang/bin/
//  # Runs a basic client *locally* that sends UDP packets of varying size.
//  # See white paper for details.
//	./client
//
//	# Specify ports to receive and send on and to.
//	./client --server_ip="192.168.2.5" --client_rcv_port=1002 --client_snd_port=1003 --server_rcv_port=2000
//
//	# Specify parameters to Pingtest. Run --help to see more.
//	./client --run_search_only=true --start_size=150 --increase_factor=3
//
//	# Randomize the UDP padding to mitigate caching.
//	./client --randomize=true
//
//  # View all flags and explanations.
//	./client --help
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
	serverIP            = flag.String("server_ip", "127.0.0.1", "The IP address of the ack server.")
	clientRcvPort       = flag.Int("client_rcv_port", 3141, "The client's port that listens for ACKs")
	clientSndPort       = flag.Int("client_snd_port", 3142, "The client's port that sends large packets.")
	serverRcvPort       = flag.Int("server_rcv_port", 3143, "The server's port that listens for packets.")
	randomize           = flag.Bool("randomize", true, "Randomize the UDP packet padding.")
	runExpIncrease      = flag.Bool("run_exp_increase", true, "Run the exponential increase phase 1 beginning.")
	runSearchOnly       = flag.Bool("run_search_only", false, "Run binary search only.")
	increaseFactor      = flag.Int("increase_factor", 10, "Increase factor during exponential increase.")
	decreaseFactor      = flag.Int("decrease_factor", 2, "Decrease factor during binary search.")
	startSize           = flag.Int("start_size", 30, "The size of the smallest packet, in bytes.")
	maxSize             = flag.Int("max_size", 64000, "The size of the largest packet, in bytes.")
	phaseOneNumPackets  = flag.Int("phase_one_num_packets", 10, "The number of packets to send during the exponential increase phase.")
	phaseTwoNumPackets  = flag.Int("phase_two_num_packets", 10, "The number of packets to send during the binary search phase.")
	lossRate            = flag.Int("loss_rate", 70, "The acceptible rate of packets to receive before increasing the packet size.")
	convergeSize        = flag.Int("converge_size", 200, "When the difference is packet sizes is less than converge_size, terminate the procedure.")
	delayBetweenPackets = flag.Int("delay_between_packets", 1, "The time, in seconds, to wait between sending packets.")
)

const (
	// maxUDPSize is the maximum size, in bytes, of a single UDP packet.
	maxUDPSize = 65000
	// timeoutMultiplier determines the timeout length for each packet. The timeout length is
	// determined by timeoutMultiplier * max(delay of smallest ping size)
	timeoutMultiplier = 5000
	// initialProbeTimeout is the default time to wait for each of the initial packets sent out to
	// determine the average delay.
	// TODO(jvesuna): Change this constant to something smarter.
	initialProbeTimeout = 10000
)

// Client holds meta-parameters to run Pingtest.
type Client struct {
	// The host ip to ping.
	IP             string
	Params         Params
	RunExpIncrease bool
	RunSearchOnly  bool
}

// Params holds parameters for Pingtest.
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

// TestStats holds statistics for an entire Pingtest speedtest experiment.
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

// computeDelays returns the min, mean, and max delays of a sorted list of packets and received
// times in milliseconds.
func computeDelays(rcvPackets []receivedPacket, rcvTimes []int64) (float64, float64, float64, error) {
	if len(rcvPackets) != len(rcvTimes) {
		return 0, 0, 0, errors.New("error: length of rcvPackets != length of rcvTimes")
	}
	if len(rcvPackets) == 0 {
		return 0, 0, 0, nil
	}
	var delays []int64
	var timeDelay float64
	for i, _ := range rcvPackets {
		timeDelay = float64(rcvTimes[i]-*rcvPackets[i].message.PingtestParams.ClientTimestampNano) / float64(1000000)
		delays = append(delays, int64(timeDelay))
	}
	sum := 0
	min, max := delays[0], delays[0]
	for _, delay := range delays {
		sum += int(delay)
		if delay < min {
			min = delay
		}
		if delay > max {
			max = delay
		}
	}
	mean := float64(sum) / float64(len(rcvPackets))
	if float64(min) > mean || mean > float64(max) {
		log.Fatalln("error: mean must be within min and max")
	}
	return float64(min), mean, float64(max), nil
}

// clientReceiver receives ping ACKs from the server and returns ping statistics.
// timeoutLen is the duration to wait for each packet in Milliseconds.
func (ps *pingStats) clientReceiver(count, timeoutLen int, ServerConn *net.UDPConn) {
	log.Println("Conn is", ServerConn)

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
				log.Println("Timeout!")
			} else {
				// TODO(jvesuna): Handle.
				log.Fatalln("Error: ", err)
			}
		} else {
			// Packet received.
			// TODO(jvesuna): Handle in goroutine with a shared struct.
			buf = buf[:numBytesRead]
			log.Println("Received packet #", i)
			rcvCount++
			message := &ptpb.PingtestMessage{}
			if err := proto.Unmarshal(buf, message); err != nil {
				log.Fatalln("error reading proto from packet:", err)
			}
			sentIndex := *message.PingtestParams.PacketIndex
			rcvPackets = append(rcvPackets, receivedPacket{
				message:   *message,
				sentIndex: int(sentIndex),
			})
			rcvTimes = append(rcvTimes, time.Now().UnixNano())
		}
	}

	// Process packets.
	sort.Sort(BySentIndex(rcvPackets))

	percentLoss := 100 - (float64(rcvCount)/float64(count))*100
	minDelay, meanDelay, maxDelay, err := computeDelays(rcvPackets, rcvTimes)
	if err != nil {
		log.Fatalln("error computing delay:", err)
	}

	ps.min = minDelay
	ps.mean = meanDelay
	ps.max = maxDelay
	ps.loss = percentLoss
	log.Println("Min Delay:", ps.min)
	log.Println("Max Delay:", ps.max)
	log.Println("Mean Delay:", ps.mean)
	log.Println("Loss Percent:", ps.loss)
	defer ps.wg.Done()
}

// runPing runs a UDP implementation of the ping util.
// count is the number of UDP packets to send.
// size is in bytes.
// timeoutLen is in milliseconds.
func runPing(count, size, timeoutLen int, Conn *net.UDPConn) (pingStats, error) {
	log.Printf("========= starting new ping =========")
	log.Println("Using byte size: ", size)

	// Startup receiver.
	var ps pingStats
	ps.wg.Add(1)
	go ps.clientReceiver(count, timeoutLen, Conn)

	// Send packets
	log.Println("sending Conn is", Conn)

	// TODO(jvesuna): Add Channel here.

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
		if err != nil {
			log.Println(err)
			return pingStats{}, errors.New("error marshalling message")
		}

		// Send the packet.
		// TODO(jvesuna):  Handle 'host down' and network problems.
		Conn.SetWriteBuffer(proto.Size(message))
		if _, err = Conn.Write(wireBytes); err != nil {
			log.Println(err)
			return pingStats{}, errors.New("error writing wireBytes to connection")
		}
		// TODO(jvesuna): Change this delay between packets?
		log.Println("Sent #", i)
		time.Sleep(time.Duration(*delayBetweenPackets) * time.Second)
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
		c.IP = "127.0.0.1"
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
func (c *Client) expIncrease(ts TestStats, startSize, maxSize, increaseFactor, timeoutLen int, Conn *net.UDPConn) (int, TestStats, error) {
	pktSize := startSize
	for {
		pktSize *= increaseFactor
		if pktSize > maxSize {
			pktSize = maxSize
		}
		ts.TotalBytesSent += c.Params.PhaseOneNumPackets * pktSize
		// TODO(jvesuna): Fix timeoutLen.
		ps, err := runPing(c.Params.PhaseOneNumPackets, pktSize, timeoutLen, Conn)
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
func (c *Client) binarySearch(ts TestStats, minBytes, maxBytes, decreaseFactor int, Conn *net.UDPConn) (int, TestStats, error) {
	log.Printf("Testing range %d to %d bytes sized packets.\n", minBytes, maxBytes)
	// middleGround is always the average of minBytes and maxBytes.
	middleGround := int(float64(maxBytes+minBytes) / float64(c.Params.DecreaseFactor))
	// oldGround was the previous middleGround, used as the new upper or lower bound in binary search.
	var oldGround int
	for {
		ts.TotalBytesSent += c.Params.PhaseTwoNumPackets * middleGround
		// TODO(jvesuna): Fix timeoutLen.
		ps, err := runPing(c.Params.PhaseTwoNumPackets, middleGround, initialProbeTimeout, Conn)
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
func (c *Client) runExpIncrease(ts TestStats, Conn *net.UDPConn) (TestStats, error) {
	// First, find the smallest RTT.
	// TODO(jvesuna): Add test runtime by taking timestamp here and at return points.
	// TODO(jvesuna): Find a better way of accumulating bytes, maybe in runPing?
	ts.TotalBytesSent += c.Params.PhaseOneNumPackets * c.Params.StartSize
	// Wait 10 seconds per packet initially.
	ps, err := runPing(c.Params.PhaseOneNumPackets, c.Params.StartSize, initialProbeTimeout, Conn)
	ts.TotalBytesDropped += int(ps.loss * 0.01 * float64(c.Params.PhaseOneNumPackets) * float64(c.Params.StartSize))
	if err != nil {
		log.Println("failed to ping host")
		return TestStats{}, err
	}
	// TODO(jvesuna): add some sanity checks
	log.Printf("Initial ping results: %v", ps)
	timeoutLen := int(math.Min(ps.max, 1)) * timeoutMultiplier

	// Begin Phase 1: exponential increase.
	pktSize, ts, err := c.expIncrease(ts, c.Params.StartSize, c.Params.MaxSize, c.Params.IncreaseFactor, timeoutLen, Conn)
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
	pktSize, ts, err = c.binarySearch(ts, minBytes, maxBytes, c.Params.DecreaseFactor, Conn)
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
func (c *Client) runSearchOnly(ts TestStats, Conn *net.UDPConn) (TestStats, error) {
	// First run smallest packet size.
	ts.TotalBytesSent += c.Params.PhaseOneNumPackets * c.Params.StartSize
	// Wait 10 seconds per packet initially.
	ps, err := runPing(c.Params.PhaseOneNumPackets, c.Params.StartSize, initialProbeTimeout, Conn)
	ts.TotalBytesDropped += int(ps.loss * 0.01 * float64(c.Params.PhaseOneNumPackets) * float64(c.Params.StartSize))
	if err != nil {
		log.Println("failed to ping host: %v", err)
		return TestStats{}, err
	}
	// TODO(jvesuna): add some sanity checks
	timeoutLen := int(math.Min(ps.max, 1)) * timeoutMultiplier
	// Then run largest packet size.
	ts.TotalBytesSent += c.Params.PhaseOneNumPackets * c.Params.MaxSize
	ps, err = runPing(c.Params.PhaseOneNumPackets, c.Params.MaxSize, timeoutLen, Conn)
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
	pktSize, ts, err := c.binarySearch(ts, c.Params.StartSize, c.Params.MaxSize, c.Params.DecreaseFactor, Conn)
	if err != nil {
		return ts, err
	}
	if pktSize == 0 {
		return TestStats{}, errors.New("pingtest: unexpected error in binary search")
	}
	return ts, nil
}

// RunPingtest runs binary search to find the largest ping packet size without a high drop rate.
// Option to run an exponential increase phase first.
// Returns the test statistics for this test.
func (c *Client) RunPingtest(Conn *net.UDPConn) (TestStats, error) {
	// First, make sure parameters are correct.
	cl, err := checkDefaults(c)
	if err != nil {
		return TestStats{}, err
	}
	// ts holds the overall statistics for this test.
	var ts TestStats

	// TODO(jvesuna): Have these functions take a range to probe for, and return a range. Then clean
	// up.
	if cl.RunExpIncrease {
		return cl.runExpIncrease(ts, Conn)
	} else if cl.RunSearchOnly {
		return cl.runSearchOnly(ts, Conn)
	}
	return TestStats{}, errors.New("pingtest failed: ended unexpectedly")
}

func main() {
	flag.Parse()

	clientSenderAddr, err := net.ResolveUDPAddr("udp", *serverIP+":"+strconv.Itoa(*serverRcvPort))
	if err != nil {
		log.Fatalln("error binding to server port:", err)
	}
	Conn, err := net.DialUDP("udp", nil, clientSenderAddr)
	if err != nil {
		log.Fatalln("error connecting UDP:", err)
	}
	defer Conn.Close()

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
		RunExpIncrease: *runExpIncrease,
		RunSearchOnly:  *runSearchOnly,
	}
	ts, err := c.RunPingtest(Conn)
	if err != nil {
		log.Println(err)
	} else {
		log.Printf("Estimated bandwidth: %f MBps, %f Kbps", ts.EstimatedMB, ts.EstimatedKb)
	}
}
