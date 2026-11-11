package telemetry

import (
	"encoding/binary"
	"net"
	"strconv"
)

type connectionEvent struct {
	startNs, endNs uint64
	endpoint       string
}

// Layout mirrors event_t in socket_latency.c (40 bytes with explicit padding).
func parseConnectionEvent(b []byte) (connectionEvent, bool) {
	if len(b) != 40 {
		return connectionEvent{}, false
	}
	start, end := binary.LittleEndian.Uint64(b[:8]), binary.LittleEndian.Uint64(b[8:16])
	if end < start {
		return connectionEvent{}, false
	}
	family := binary.LittleEndian.Uint16(b[34:36])
	var ip net.IP
	switch family {
	case 2:
		ip = net.IP(b[16:20])
	case 10:
		ip = net.IP(b[16:32])
	default:
		return connectionEvent{}, false
	}
	port := binary.LittleEndian.Uint16(b[32:34])
	return connectionEvent{start, end, net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))}, true
}

// ObserveConnection counts completed backend TCP connections, separately from
// request rewards. Callers resolve destination addresses to registered pods.
func (c *Collector) ObserveConnection(podID string, seconds float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := c.connections[podID]
	stats.Count++
	stats.Seconds += seconds
	c.connections[podID] = stats
}

type ConnectionStats struct {
	Count   uint64
	Seconds float64
}

func (c *Collector) Connections() map[string]ConnectionStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make(map[string]ConnectionStats, len(c.connections))
	for pod, stats := range c.connections {
		result[pod] = stats
	}
	return result
}
