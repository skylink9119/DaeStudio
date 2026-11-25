package daemon

import (
	"bufio"
	"context"
	"io"
	"math"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EndpointTarget identifies a configured proxy node server host and port.
type EndpointTarget struct {
	Key  string
	Host string
	Port uint16
}

// EndpointTraffic reports live throughput and cumulative byte counters for a node endpoint.
type EndpointTraffic struct {
	Endpoint      string `json:"endpoint"`
	UploadBps     int64  `json:"uploadBps"`
	DownloadBps   int64  `json:"downloadBps"`
	TotalUpload   int64  `json:"totalUpload"`
	TotalDownload int64  `json:"totalDownload"`
	ActiveConns   int    `json:"activeConns"`
}

// NormalizeEndpointKey returns canonical "host:port" (lowercase host without IPv6 brackets).
func NormalizeEndpointKey(host string, port int) string {
	clean := strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if clean == "" || port <= 0 || port > 65535 {
		return ""
	}
	return clean + ":" + strconv.Itoa(port)
}

type addrPortKey struct {
	addr netip.Addr
	port uint16
}

type flowKey struct {
	proto     uint8
	origSrc   netip.Addr
	origDst   netip.Addr
	origSport uint16
	origDport uint16
}

type flowBytes struct {
	tx uint64
	rx uint64
}

type domainCacheEntry struct {
	updatedAt time.Time
	ips       map[netip.Addr]time.Time
}

type endpointTotals struct {
	tx uint64
	rx uint64
}

type nodeTrafficTracker struct {
	mu              sync.Mutex
	acctOnce        sync.Once
	targets         []EndpointTarget
	targetSig       string
	hostCache       map[string]*domainCacheEntry
	resolving       bool
	seededAddrPorts map[addrPortKey]bool
	prevFlows       map[flowKey]flowBytes
	totals          map[string]endpointTotals
	lastSample      time.Time
	lastSnapshot    map[string]EndpointTraffic
}

func newNodeTrafficTracker() *nodeTrafficTracker {
	return &nodeTrafficTracker{
		hostCache:       map[string]*domainCacheEntry{},
		seededAddrPorts: map[addrPortKey]bool{},
		prevFlows:       map[flowKey]flowBytes{},
		totals:          map[string]endpointTotals{},
		lastSnapshot:    map[string]EndpointTraffic{},
	}
}

func (t *nodeTrafficTracker) ensureConntrackAcct() {
	if runtime.GOOS != "linux" {
		return
	}
	t.acctOnce.Do(func() {
		const acctSysctl = "/proc/sys/net/netfilter/nf_conntrack_acct"
		b, err := os.ReadFile(acctSysctl)
		if err == nil && strings.TrimSpace(string(b)) == "0" {
			_ = os.WriteFile(acctSysctl, []byte("1\n"), 0o644)
		}
	})
}

func (t *nodeTrafficTracker) SyncTargets(raw []EndpointTarget) {
	seen := make(map[string]EndpointTarget, len(raw))
	var keys []string
	for _, item := range raw {
		cleanHost := strings.ToLower(strings.Trim(strings.TrimSpace(item.Host), "[]"))
		if cleanHost == "" || item.Port == 0 {
			continue
		}
		key := cleanHost + ":" + strconv.Itoa(int(item.Port))
		if _, exists := seen[key]; !exists {
			seen[key] = EndpointTarget{Key: key, Host: cleanHost, Port: item.Port}
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	sig := strings.Join(keys, "|")

	var unresolvedDomains []string
	now := time.Now()

	t.mu.Lock()
	if sig != t.targetSig {
		t.targetSig = sig
		t.targets = make([]EndpointTarget, 0, len(keys))
		validKeys := make(map[string]bool, len(keys))
		for _, k := range keys {
			t.targets = append(t.targets, seen[k])
			validKeys[k] = true
		}
		for existingKey := range t.totals {
			if !validKeys[existingKey] {
				delete(t.totals, existingKey)
			}
		}
	}

	domainSeen := map[string]bool{}
	needInitialWait := false
	for _, target := range t.targets {
		if _, err := netip.ParseAddr(target.Host); err == nil {
			continue
		}
		if domainSeen[target.Host] {
			continue
		}
		domainSeen[target.Host] = true
		entry := t.hostCache[target.Host]
		if entry == nil {
			unresolvedDomains = append(unresolvedDomains, target.Host)
			needInitialWait = true
		} else if now.Sub(entry.updatedAt) >= 60*time.Second {
			unresolvedDomains = append(unresolvedDomains, target.Host)
		}
	}

	for host := range t.hostCache {
		if !domainSeen[host] {
			delete(t.hostCache, host)
		}
	}
	shouldLaunch := len(unresolvedDomains) > 0 && !t.resolving
	if shouldLaunch {
		t.resolving = true
	}
	t.mu.Unlock()

	if !shouldLaunch {
		return
	}

	done := make(chan struct{})
	go func(domains []string) {
		defer close(done)
		t.resolveDomains(domains)
	}(unresolvedDomains)

	if needInitialWait {
		select {
		case <-done:
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (t *nodeTrafficTracker) resolveDomains(domains []string) {
	defer func() {
		t.mu.Lock()
		t.resolving = false
		t.mu.Unlock()
	}()

	type lookupOut struct {
		host  string
		addrs []netip.Addr
	}
	results := make(chan lookupOut, len(domains))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup

	for _, host := range domains {
		sem <- struct{}{}
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", h)
			if err == nil && len(ips) > 0 {
				results <- lookupOut{host: h, addrs: ips}
			} else {
				results <- lookupOut{host: h}
			}
		}(host)
	}
	wg.Wait()
	close(results)

	now := time.Now()
	const retention = 15 * time.Minute

	t.mu.Lock()
	defer t.mu.Unlock()
	activeHosts := map[string]bool{}
	for _, target := range t.targets {
		activeHosts[target.Host] = true
	}
	for res := range results {
		if !activeHosts[res.host] {
			continue
		}
		entry := t.hostCache[res.host]
		if entry == nil {
			entry = &domainCacheEntry{ips: map[netip.Addr]time.Time{}}
			t.hostCache[res.host] = entry
		}
		entry.updatedAt = now
		for _, ip := range res.addrs {
			if ip.IsValid() {
				entry.ips[ip.Unmap()] = now
			}
		}
		for ip, lastSeen := range entry.ips {
			if now.Sub(lastSeen) > retention {
				delete(entry.ips, ip)
			}
		}
	}
}

func cloneTrafficMap(src map[string]EndpointTraffic) map[string]EndpointTraffic {
	if len(src) == 0 {
		return map[string]EndpointTraffic{}
	}
	out := make(map[string]EndpointTraffic, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (t *nodeTrafficTracker) buildLookupLocked() (map[addrPortKey][]string, map[uint16]struct{}, []EndpointTarget) {
	ipPortMap := make(map[addrPortKey][]string, len(t.targets)*2)
	ports := make(map[uint16]struct{}, len(t.targets))
	targetsCopy := make([]EndpointTarget, len(t.targets))
	copy(targetsCopy, t.targets)

	addMapping := func(ip netip.Addr, port uint16, epKey string) {
		ap := addrPortKey{addr: ip.Unmap(), port: port}
		existing := ipPortMap[ap]
		for _, k := range existing {
			if k == epKey {
				return
			}
		}
		ipPortMap[ap] = append(existing, epKey)
		ports[port] = struct{}{}
	}

	for _, target := range t.targets {
		if ip, err := netip.ParseAddr(target.Host); err == nil {
			addMapping(ip, target.Port, target.Key)
			continue
		}
		if entry := t.hostCache[target.Host]; entry != nil {
			for ip := range entry.ips {
				addMapping(ip, target.Port, target.Key)
			}
		}
	}
	return ipPortMap, ports, targetsCopy
}

func (t *nodeTrafficTracker) Sample(running, mockMode bool, tickSeq uint64) map[string]EndpointTraffic {
	if mockMode {
		t.mu.Lock()
		defer t.mu.Unlock()
		if !running || len(t.targets) == 0 {
			return map[string]EndpointTraffic{}
		}
		primary := t.targets[0]
		tf := float64(tickSeq)
		down := int64(760_000 + 580_000*math.Sin(tf*0.45))
		up := int64(165_000 + 95_000*math.Cos(tf*0.55))
		if down < 24_000 {
			down = 24_000
		}
		if up < 8_000 {
			up = 8_000
		}
		tot := t.totals[primary.Key]
		tot.rx += uint64(down)
		tot.tx += uint64(up)
		t.totals[primary.Key] = tot
		return map[string]EndpointTraffic{
			primary.Key: {
				Endpoint:      primary.Key,
				UploadBps:     up,
				DownloadBps:   down,
				TotalUpload:   int64(tot.tx),
				TotalDownload: int64(tot.rx),
				ActiveConns:   6,
			},
		}
	}

	if runtime.GOOS != "linux" || !running {
		return map[string]EndpointTraffic{}
	}

	t.ensureConntrackAcct()

	now := time.Now()
	t.mu.Lock()
	if !t.lastSample.IsZero() && now.Sub(t.lastSample) < 800*time.Millisecond {
		cached := cloneTrafficMap(t.lastSnapshot)
		t.mu.Unlock()
		return cached
	}
	ipPortMap, trackedPorts, targetsCopy := t.buildLookupLocked()
	t.mu.Unlock()

	if len(targetsCopy) == 0 || len(trackedPorts) == 0 {
		return map[string]EndpointTraffic{}
	}

	f, err := os.Open("/proc/net/nf_conntrack")
	if err != nil {
		return map[string]EndpointTraffic{}
	}
	defer f.Close()

	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.lastSample.IsZero() && now.Sub(t.lastSample) < 800*time.Millisecond {
		return cloneTrafficMap(t.lastSnapshot)
	}
	return t.sampleFromReaderLocked(f, now, ipPortMap, trackedPorts, targetsCopy)
}

func (t *nodeTrafficTracker) sampleFromReaderLocked(
	r io.Reader,
	now time.Time,
	ipPortMap map[addrPortKey][]string,
	trackedPorts map[uint16]struct{},
	targets []EndpointTarget,
) map[string]EndpointTraffic {
	var dt float64
	if !t.lastSample.IsZero() {
		dt = now.Sub(t.lastSample).Seconds()
	}

	nextFlows := make(map[flowKey]flowBytes, len(t.prevFlows)+16)
	deltaTxByEP := make(map[string]uint64, len(targets))
	deltaRxByEP := make(map[string]uint64, len(targets))
	connsByEP := make(map[string]int, len(targets))
	newlySeeded := make(map[addrPortKey]bool)

	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 256*1024)

	linesRead := 0
	for scanner.Scan() {
		linesRead++
		if linesRead > 65536 {
			break
		}
		line := scanner.Text()
		fk, curBytes, active, ok := parseConntrackProxyFlow(line, trackedPorts, ipPortMap)
		if !ok {
			continue
		}

		ap := addrPortKey{addr: fk.origDst, port: fk.origDport}
		endpoints := ipPortMap[ap]
		if len(endpoints) == 0 {
			continue
		}

		// Avoid duplicates if kernel dumps identical tuple twice during table resize
		if _, dup := nextFlows[fk]; dup {
			continue
		}
		nextFlows[fk] = curBytes

		if active {
			for _, ep := range endpoints {
				connsByEP[ep]++
			}
		}

		if !t.seededAddrPorts[ap] {
			newlySeeded[ap] = true
			continue
		}

		var dTx, dRx uint64
		if prev, existed := t.prevFlows[fk]; existed {
			if curBytes.tx >= prev.tx {
				dTx = curBytes.tx - prev.tx
			} else {
				dTx = curBytes.tx
			}
			if curBytes.rx >= prev.rx {
				dRx = curBytes.rx - prev.rx
			} else {
				dRx = curBytes.rx
			}
		} else if dt > 0 {
			// Brand-new connection opened after this endpoint was already seeded
			dTx = curBytes.tx
			dRx = curBytes.rx
		}

		if dTx > 0 || dRx > 0 {
			for _, ep := range endpoints {
				deltaTxByEP[ep] += dTx
				deltaRxByEP[ep] += dRx
			}
		}
	}

	for ap := range t.seededAddrPorts {
		if _, ok := ipPortMap[ap]; !ok {
			delete(t.seededAddrPorts, ap)
		}
	}
	for ap := range ipPortMap {
		t.seededAddrPorts[ap] = true
	}
	for ap := range newlySeeded {
		t.seededAddrPorts[ap] = true
	}
	t.prevFlows = nextFlows
	t.lastSample = now

	snapshot := make(map[string]EndpointTraffic, len(targets))
	for _, target := range targets {
		dTx := deltaTxByEP[target.Key]
		dRx := deltaRxByEP[target.Key]
		tot := t.totals[target.Key]
		tot.tx += dTx
		tot.rx += dRx
		t.totals[target.Key] = tot

		var upBps, downBps int64
		if dt > 0.05 {
			upBps = int64(float64(dTx) / dt)
			downBps = int64(float64(dRx) / dt)
		}
		conns := connsByEP[target.Key]
		if upBps > 0 || downBps > 0 || tot.tx > 0 || tot.rx > 0 || conns > 0 {
			snapshot[target.Key] = EndpointTraffic{
				Endpoint:      target.Key,
				UploadBps:     upBps,
				DownloadBps:   downBps,
				TotalUpload:   int64(tot.tx),
				TotalDownload: int64(tot.rx),
				ActiveConns:   conns,
			}
		}
	}

	t.lastSnapshot = snapshot
	return cloneTrafficMap(snapshot)
}

func extractKVValue(s, prefix string) (string, string, bool) {
	idx := strings.Index(s, prefix)
	if idx < 0 {
		return "", "", false
	}
	rest := s[idx+len(prefix):]
	end := strings.IndexByte(rest, ' ')
	if end < 0 {
		return rest, "", true
	}
	return rest[:end], rest[end+1:], true
}

func parseUint16Fast(s string) (uint16, bool) {
	if len(s) == 0 || len(s) > 5 {
		return 0, false
	}
	var n uint32
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint32(c-'0')
	}
	if n == 0 || n > 65535 {
		return 0, false
	}
	return uint16(n), true
}

func parseUint64Fast(s string) (uint64, bool) {
	if len(s) == 0 || len(s) > 20 {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		digit := uint64(c - '0')
		if n > (math.MaxUint64-digit)/10 {
			return 0, false
		}
		n = n*10 + digit
	}
	return n, true
}

// parseConntrackProxyFlow parses a single /proc/net/nf_conntrack line if and only if
// its original destination (dst:dport) matches a tracked proxy endpoint.
func parseConntrackProxyFlow(
	line string,
	trackedPorts map[uint16]struct{},
	ipPortMap map[addrPortKey][]string,
) (flowKey, flowBytes, bool, bool) {
	// Fast path 1: check first dport= before parsing IPs or splitting tokens
	dportStr, afterOrigDport, ok := extractKVValue(line, "dport=")
	if !ok {
		return flowKey{}, flowBytes{}, false, false
	}
	origDport, ok := parseUint16Fast(dportStr)
	if !ok {
		return flowKey{}, flowBytes{}, false, false
	}
	if _, tracked := trackedPorts[origDport]; !tracked {
		return flowKey{}, flowBytes{}, false, false
	}

	// Fast path 2: check first dst= against tracked IP+Port map
	origSrcStr, afterOrigSrc, ok := extractKVValue(line, "src=")
	if !ok {
		return flowKey{}, flowBytes{}, false, false
	}
	origDstStr, afterOrigDst, ok := extractKVValue(afterOrigSrc, "dst=")
	if !ok {
		return flowKey{}, flowBytes{}, false, false
	}
	origDst, err := netip.ParseAddr(origDstStr)
	if err != nil {
		return flowKey{}, flowBytes{}, false, false
	}
	origDst = origDst.Unmap()
	if len(ipPortMap[addrPortKey{addr: origDst, port: origDport}]) == 0 {
		return flowKey{}, flowBytes{}, false, false
	}

	origSrc, err := netip.ParseAddr(origSrcStr)
	if err != nil {
		return flowKey{}, flowBytes{}, false, false
	}
	origSrc = origSrc.Unmap()

	sportStr, _, ok := extractKVValue(afterOrigDst, "sport=")
	if !ok {
		return flowKey{}, flowBytes{}, false, false
	}
	origSport, ok := parseUint16Fast(sportStr)
	if !ok {
		return flowKey{}, flowBytes{}, false, false
	}

	// Original direction bytes (Upload TX from router to proxy node)
	txBytesStr, afterOrigBytes, ok := extractKVValue(afterOrigDport, "bytes=")
	if !ok {
		return flowKey{}, flowBytes{}, false, false
	}
	txBytes, ok := parseUint64Fast(txBytesStr)
	if !ok {
		return flowKey{}, flowBytes{}, false, false
	}

	// Reply direction bytes (Download RX from proxy node to router)
	rxBytesStr, _, ok := extractKVValue(afterOrigBytes, "bytes=")
	if !ok {
		return flowKey{}, flowBytes{}, false, false
	}
	rxBytes, ok := parseUint64Fast(rxBytesStr)
	if !ok {
		return flowKey{}, flowBytes{}, false, false
	}

	var proto uint8 = 6
	isUDP := strings.Contains(line[:minInt(len(line), 36)], " udp ")
	if isUDP {
		proto = 17
	}
	active := false
	if isUDP {
		active = !strings.Contains(line, "[UNREPLIED]")
	} else {
		active = strings.Contains(line, "ESTABLISHED")
	}

	return flowKey{
		proto:     proto,
		origSrc:   origSrc,
		origDst:   origDst,
		origSport: origSport,
		origDport: origDport,
	}, flowBytes{tx: txBytes, rx: rxBytes}, active, true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
