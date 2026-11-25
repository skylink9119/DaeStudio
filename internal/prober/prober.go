package prober

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"daed-next/internal/model"
)

// Result holds the active latency check result for one node.
type Result struct {
	ID        string `json:"id"`
	LatencyMs int    `json:"latencyMs"`
	Alive     bool   `json:"alive"`
	CheckedAt string `json:"checkedAt"`
	Error     string `json:"error,omitempty"`
}

// SiteCheck holds real-world end-to-end connectivity probe result from the router.
type SiteCheck struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status"`
	LatencyMs int    `json:"latencyMs"`
}

// ProbeNodes performs concurrent TCP handshake latency checks across nodes with bounded concurrency.
func ProbeNodes(nodes []model.Node, timeout time.Duration, concurrency int) []Result {
	if concurrency <= 0 {
		concurrency = 16
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	results := make([]Result, len(nodes))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for idx, n := range nodes {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, node model.Node) {
			defer wg.Done()
			defer func() { <-sem }()

			results[i] = ProbeSingle(node, timeout)
		}(idx, n)
	}
	wg.Wait()
	return results
}

// ProbeSingle measures TCP connect RTT to a node's server:port endpoint.
func ProbeSingle(node model.Node, timeout time.Duration) Result {
	now := time.Now().Format(time.RFC3339)
	if node.Address == "" || node.Port <= 0 {
		return Result{
			ID:        node.ID,
			LatencyMs: 0,
			Alive:     false,
			CheckedAt: now,
			Error:     "无效节点地址或端口",
		}
	}

	target := net.JoinHostPort(node.Address, strconv.Itoa(node.Port))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", target, timeout)
	elapsed := time.Since(start)
	if err != nil {
		return Result{
			ID:        node.ID,
			LatencyMs: 0,
			Alive:     false,
			CheckedAt: now,
			Error:     fmt.Sprintf("连接超时或拒绝: %v", err),
		}
	}
	_ = conn.Close()
	ms := int(elapsed.Milliseconds())
	if ms < 1 {
		ms = 1
	}
	return Result{
		ID:        node.ID,
		LatencyMs: ms,
		Alive:     true,
		CheckedAt: now,
	}
}

// ProbeConnectivity tests real HTTP/HTTPS connectivity from the router to domestic and global endpoints.
func ProbeConnectivity() []SiteCheck {
	targets := []SiteCheck{
		{ID: "cn", Name: "国内直连 · 百度", URL: "https://www.baidu.com"},
		{ID: "cf", Name: "海外连通 · Cloudflare", URL: "http://cp.cloudflare.com"},
		{ID: "google", Name: "Google", URL: "https://www.google.com/generate_204"},
		{ID: "github", Name: "GitHub", URL: "https://github.com"},
	}

	client := &http.Client{
		Timeout: 4500 * time.Millisecond,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			t0 := time.Now()
			req, err := http.NewRequest("HEAD", targets[idx].URL, nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; DaeStudio Connectivity Check)")
			resp, err := client.Do(req)
			ms := int(time.Since(t0).Milliseconds())
			if err != nil {
				targets[idx].OK = false
				targets[idx].LatencyMs = 0
				return
			}
			_ = resp.Body.Close()
			if ms < 1 {
				ms = 1
			}
			targets[idx].Status = resp.StatusCode
			targets[idx].OK = resp.StatusCode > 0 && resp.StatusCode < 500
			targets[idx].LatencyMs = ms
		}(i)
	}
	wg.Wait()
	return targets
}
