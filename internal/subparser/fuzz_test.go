package subparser

import "testing"

func FuzzSubscriptionParser(f *testing.F) {
	for _, s := range []string{"", "{\"outbounds\":[]}", "proxies:\n - name: test", "trojan://demo@example.com:443#test", "%%%"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 65536 {
			t.Skip()
		}
		ParseSubscriptionContent(s, "test", "test", "")
	})
}
