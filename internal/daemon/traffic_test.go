package daemon

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestConntrackPerFlowDeltaIPv6AndFlowExpiry(t *testing.T) {
	tracker := newNodeTrafficTracker()
	epKey := NormalizeEndpointKey("[2605:52c0:2:41d5:be24:11ff:fe3c:2a0c]", 443)
	if epKey != "2605:52c0:2:41d5:be24:11ff:fe3c:2a0c:443" {
		t.Fatalf("unexpected normalized endpoint key: %q", epKey)
	}

	targets := []EndpointTarget{
		{Key: epKey, Host: "2605:52c0:2:41d5:be24:11ff:fe3c:2a0c", Port: 443},
	}
	tracker.targets = targets
	ipPortMap, trackedPorts, targetsCopy := tracker.buildLookupLocked()

	t0 := time.Now()
	// Sample 1: Seed baseline with an existing 12MB flow (sport=49082) + a 5MB TIME_WAIT flow (sport=45730).
	// Note expanded zero-padded IPv6 format from Linux /proc/net/nf_conntrack!
	sample1 := strings.Join([]string{
		"ipv6     10 tcp      6 7439 ESTABLISHED src=2409:8a55:d150:2170:0000:0000:0000:0001 dst=2605:52c0:0002:41d5:be24:11ff:fe3c:2a0c sport=49082 dport=443 packets=3080 bytes=200000 src=2605:52c0:0002:41d5:be24:11ff:fe3c:2a0c dst=2409:8a55:d150:2170:0000:0000:0000:0001 sport=443 dport=49082 packets=2741 bytes=12000000 [ASSURED] mark=0 zone=0 use=2",
		"ipv6     10 tcp      6 2 TIME_WAIT src=2409:8a55:d150:2170:0000:0000:0000:0001 dst=2605:52c0:0002:41d5:be24:11ff:fe3c:2a0c sport=45730 dport=443 packets=400 bytes=50000 src=2605:52c0:0002:41d5:be24:11ff:fe3c:2a0c dst=2409:8a55:d150:2170:0000:0000:0000:0001 sport=443 dport=45730 packets=3900 bytes=5000000 [ASSURED] mark=0 zone=0 use=2",
	}, "\n")

	snap1 := tracker.sampleFromReaderLocked(strings.NewReader(sample1), t0, ipPortMap, trackedPorts, targetsCopy)
	if snap1[epKey].DownloadBps != 0 || snap1[epKey].UploadBps != 0 || snap1[epKey].ActiveConns != 1 {
		t.Fatalf("expected seeded baseline 0 B/s and 1 active conn, got %+v", snap1[epKey])
	}

	// Sample 2 (2.0s later):
	// - Old 5MB TIME_WAIT flow (sport=45730) EXPIRED and disappeared!
	// - Existing ESTABLISHED flow (sport=49082) grew by +1,000,000 RX bytes and +100,000 TX bytes.
	// - A brand-new short-lived FIN_WAIT burst flow (sport=45300) downloaded +2,000,000 RX bytes and +100,000 TX bytes!
	// Total delta in 2.0s MUST be 3,000,000 RX (1,500,000 B/s) and 200,000 TX (100,000 B/s), with ZERO negative dip from expired flow!
	sample2 := strings.Join([]string{
		"ipv6     10 tcp      6 7439 ESTABLISHED src=2409:8a55:d150:2170:0000:0000:0000:0001 dst=2605:52c0:0002:41d5:be24:11ff:fe3c:2a0c sport=49082 dport=443 packets=3500 bytes=300000 src=2605:52c0:0002:41d5:be24:11ff:fe3c:2a0c dst=2409:8a55:d150:2170:0000:0000:0000:0001 sport=443 dport=49082 packets=3500 bytes=13000000 [ASSURED] mark=0 zone=0 use=2",
		"ipv6     10 tcp      6 119 FIN_WAIT src=2409:8a55:d150:2170:0000:0000:0000:0001 dst=2605:52c0:0002:41d5:be24:11ff:fe3c:2a0c sport=45300 dport=443 packets=1367 bytes=100000 src=2605:52c0:0002:41d5:be24:11ff:fe3c:2a0c dst=2409:8a55:d150:2170:0000:0000:0000:0001 sport=443 dport=45300 packets=1428 bytes=2000000 [ASSURED] mark=0 zone=0 use=2",
	}, "\n")

	snap2 := tracker.sampleFromReaderLocked(strings.NewReader(sample2), t0.Add(2*time.Second), ipPortMap, trackedPorts, targetsCopy)
	got := snap2[epKey]
	if got.DownloadBps != 1_500_000 || got.UploadBps != 100_000 {
		t.Fatalf("expected DownloadBps=1500000 UploadBps=100000, got %+v", got)
	}
	if got.TotalDownload != 3_000_000 || got.TotalUpload != 200_000 {
		t.Fatalf("expected TotalDownload=3000000 TotalUpload=200000, got %+v", got)
	}

	// Verify UDP (Hysteria2 / TUIC) flow parsing as well
	udpMap := map[addrPortKey][]string{
		{addr: netip.MustParseAddr("1.0.0.1"), port: 8443}: {"1.0.0.1:8443"},
	}
	udpPorts := map[uint16]struct{}{8443: {}}
	udpLine := "ipv4     2 udp      17 179 src=172.29.37.51 dst=1.0.0.1 sport=54321 dport=8443 packets=120 bytes=98304 src=1.0.0.1 dst=172.29.37.51 sport=8443 dport=54321 packets=210 bytes=245120 [ASSURED] mark=0 zone=0 use=2"
	fk, fb, active, ok := parseConntrackProxyFlow(udpLine, udpPorts, udpMap)
	if !ok || !active || fk.proto != 17 || fb.tx != 98304 || fb.rx != 245120 {
		t.Fatalf("unexpected UDP conntrack parse: ok=%v active=%v fk=%+v fb=%+v", ok, active, fk, fb)
	}
}

func TestTrafficCounterOverflowRejected(t *testing.T) {
	if _, ok := parseUint64Fast("18446744073709551616"); ok {
		t.Fatal("uint64 overflow accepted")
	}
	if n, ok := parseUint64Fast("18446744073709551615"); !ok || n != ^uint64(0) {
		t.Fatal("valid max rejected")
	}
}

func TestTrafficDropsRemovedHostCache(t *testing.T) {
	tracker := newNodeTrafficTracker()
	tracker.hostCache["removed.example"] = &domainCacheEntry{}
	tracker.SyncTargets(nil)
	if len(tracker.hostCache) != 0 {
		t.Fatal("removed hosts retained")
	}
}
