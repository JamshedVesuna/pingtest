// Package pingtest implements a lightweight bandwidth estimation using 'ping.'
package main

import (
	"errors"
	"log"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
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
}

// buildRegex returns regular expressions for parsing ping output.
func buildRegex() (*regexp.Regexp, *regexp.Regexp, *regexp.Regexp, *regexp.Regexp) {
	// Capture any float.
	reNum := regexp.MustCompile("\\d*\\.?\\d+?")
	// Capture the % packet lost.
	reLoss := regexp.MustCompile("\\d*\\.?\\d% packet loss")
	// Capture the entire summary for a set of pings.
	reSummary := regexp.MustCompile("\\d*\\.?\\d*\\/\\d*\\.?\\d*\\/\\d*\\.?\\d*\\/\\d*\\.?\\d*")
	// Capture an error if the ping failed and the network is down.
	netOut := regexp.MustCompile("Network is unreachable")
	return reNum, reLoss, reSummary, netOut
}

// parseSummary parses the summary statistics from a ping output.
func parseSummary(resSummary []string) (float64, float64, float64, float64) {
	minOut, err := strconv.ParseFloat(resSummary[0], 64)
	if err != nil {
		log.Fatalf("failed to parse summary statistics: %v", err)
	}
	meanOut, err := strconv.ParseFloat(resSummary[1], 64)
	if err != nil {
		log.Fatalf("failed to parse summary statistics: %v", err)
	}
	maxOut, err := strconv.ParseFloat(resSummary[2], 64)
	if err != nil {
		log.Fatalf("failed to parse summary statistics: %v", err)
	}
	stdevOut, err := strconv.ParseFloat(resSummary[3], 64)
	if err != nil {
		log.Fatalf("failed to parse summary statistics: %v", err)
	}
	return minOut, meanOut, maxOut, stdevOut
}

// runPing runs the Unix version of ping and returns a pingStats.
func runPing(ip string, count, size int) (pingStats, error) {
	log.Printf("========= starting new ping =========")

	// Define regex.
	reNum, reLoss, reSummary, netOut := buildRegex()

	// Run the ping.
	// TODO(jvesuna): Change this to an ICMP or UDP packet instead of relying on system tools.
	// Optimized for OS X ping util.
	out, err := exec.Command("ping", "-i", "1", "-c", strconv.Itoa(count), "-s",
		strconv.Itoa(size), ip).Output()
	if err != nil {
		// Test 3 different kinds of errors.
		// 1) Network down
		resOut := netOut.FindString(string(out))
		if len(resOut) != 0 {
			return pingStats{}, errors.New("Network is unreachable")
		}
		// 2) Or 100% packet loss.
		resLoss, err2 := strconv.ParseFloat(reNum.FindString(reLoss.FindString(string(out))), 64)
		if err2 != nil {
			log.Printf("Error: %v", err2)
		}
		if resLoss == 100 {
			return pingStats{loss: 100}, nil
		}
		// 3) Something else.
		log.Fatalf("Ping Error: %v\nOutput:\n%v\n", err, string(out))
		return pingStats{}, err
	}
	// Print the output from running the ping.
	log.Printf("%s\n", out)

	// resLoss is the packet loss %.
	resLoss, err := strconv.ParseFloat(reNum.FindString(reLoss.FindString(string(out))), 64)
	// resSummary is the summary statistics from the ping output.
	resSummary := strings.Split(reSummary.FindString(string(out)), "/")
	minOut, meanOut, maxOut, stdevOut := parseSummary(resSummary)

	return pingStats{
		min:   minOut,
		mean:  meanOut,
		max:   maxOut,
		stdev: stdevOut,
		loss:  resLoss,
	}, nil
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
		c.Params.StartSize = 30
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
		ps, err := runPing(c.IP, c.Params.PhaseOneNumPackets, pktSize)
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
		ps, err := runPing(c.IP, c.Params.PhaseTwoNumPackets, middleGround)
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
	ps, err := runPing(c.IP, c.Params.PhaseOneNumPackets, c.Params.StartSize)
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
	ps, err := runPing(c.IP, c.Params.PhaseOneNumPackets, c.Params.StartSize)
	ts.TotalBytesDropped += int(ps.loss * 0.01 * float64(c.Params.PhaseOneNumPackets) * float64(c.Params.StartSize))
	if err != nil {
		log.Fatalf("failed to ping host: %v", err)
		return TestStats{}, err
	}
	// Then run largest packet size.
	ts.TotalBytesSent += c.Params.PhaseOneNumPackets * c.Params.MaxSize
	ps, err = runPing(c.IP, c.Params.PhaseOneNumPackets, c.Params.MaxSize)
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
	c := Client{RunExpIncrease: true}
	ts, err := c.RunPingtest()
	if err != nil {
		log.Println(err)
	}
	log.Printf("Estimated bandwidth: %f MBps, %f Kbps", ts.EstimatedMB, ts.EstimatedKb)
}
