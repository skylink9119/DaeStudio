package compiler

import (
	"strings"
	"testing"
)

func TestGeneratedNodeDNSRoundTrip(t *testing.T) {
	raw := "upstream {\n cndns: 'udp://223.5.5.5:53'\n}\nrouting {\n request {\n # [Auto:NodeDomains] generated\n qname(full:old.example) -> cndns\n qname(full:manual.example) -> cndns\n fallback: cndns\n }\n}"
	clean := stripGeneratedNodeDNS(raw)
	if strings.Contains(clean, "old.example") || !strings.Contains(clean, "manual.example") {
		t.Fatal("must remove generated rules only")
	}
	if stripGeneratedNodeDNS(clean) != clean {
		t.Fatal("cleanup must be idempotent")
	}
	dns := ParseDNSDSL(raw)
	if len(dns.RequestRules) != 1 || !strings.Contains(dns.RequestRules[0].Expr, "manual.example") {
		t.Fatal("visual conversion persisted generated rule")
	}
}
