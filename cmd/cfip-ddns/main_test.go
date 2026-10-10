package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConfiguration(t *testing.T) {
	for _, key := range []string{"MIN_SPEED", "MAX_LATENCY", "ALIYUN_DOMAIN", "ALIYUN_ACCESS_KEY_ID", "ALIYUN_ACCESS_KEY_SECRET", "DOWNLOAD_NUMBER", "DOWNLOAD_URL", "CFST_PATH", "IP_FILE", "DATA_DIR", "CHECK_INTERVAL", "CFST_TIMEOUT", "DNS_TTL"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	file := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(file, []byte("MIN_SPEED=5\nMAX_LATENCY=150\nALIYUN_DOMAIN=cf.example.com\nALIYUN_ACCESS_KEY_ID=env-id\nALIYUN_ACCESS_KEY_SECRET='env-secret'\nDOWNLOAD_NUMBER=7\nDOWNLOAD_URL=https://env.example.com/file\n"), 0600)
	original := os.Args
	defer func() { os.Args = original }()
	cases := []struct {
		args  []string
		speed float64
		id    string
		n     int
	}{
		{nil, 5, "env-id", 7},
		{[]string{"--min-speed=99", "--aliyun-access-key-id=partial"}, 5, "env-id", 7},
		{[]string{"--min-speed=8", "--max-latency=100", "--aliyun-domain=cli.example.com", "--aliyun-access-key-id=cli-id", "--aliyun-access-key-secret=cli-secret", "--download-number=2"}, 8, "cli-id", 2},
	}
	for _, tc := range cases {
		os.Args = append([]string{"cfip-ddns", "--env-file", file}, tc.args...)
		c, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if c.MinSpeed != tc.speed || c.KeyID != tc.id || c.DownloadNumber != tc.n {
			t.Fatalf("unexpected configuration: speed=%v id=%v n=%v", c.MinSpeed, c.KeyID, c.DownloadNumber)
		}
	}
	os.Args = []string{"cfip-ddns", "--env-file", file, "--download-number=3", "--download-url=https://cli.example.com/file"}
	c, err := loadConfig()
	if err != nil || c.DownloadNumber != 3 || c.DownloadURL != "https://cli.example.com/file" || c.MinSpeed != 5 {
		t.Fatal("independent download options did not override .env")
	}
	os.Args = []string{"cfip-ddns", "--env-file", file}
	c, err = loadConfig()
	if err != nil || c.DownloadURL != "https://env.example.com/file" {
		t.Fatal("DOWNLOAD_URL was not read from .env")
	}
	t.Setenv("MIN_SPEED", "6")
	os.Args = []string{"cfip-ddns", "--env-file", file}
	c, err = loadConfig()
	if err != nil || c.MinSpeed != 6 {
		t.Fatal("process env should override .env")
	}
	os.Args = []string{"cfip-ddns", "--env-file", file, "--min-speed=NaN", "--max-latency=100", "--aliyun-domain=cf.example.com", "--aliyun-access-key-id=x", "--aliyun-access-key-secret=x"}
	if _, err = loadConfig(); err == nil {
		t.Fatal("NaN accepted")
	}
}

const csvHeader = "IP 地址,已发送,已接收,丢包率,平均延迟,下载速度\n"

func TestCSVAndBestCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.csv")
	os.WriteFile(path, []byte(csvHeader+"1.1.1.1,4,4,0,20,8\n1.0.0.1,4,4,0,999,9\n8.8.8.8,4,4,0,10,1\n9.9.9.9,4,2,0.5,10,9\n8.8.4.4,4,4,0,NaN,9\n"), 0600)
	rows, err := readResults(path, 5, 100, true)
	if err != nil || len(rows) != 1 || rows[0].IP != "1.1.1.1" {
		t.Fatalf("filtered CSV: %v %v", rows, err)
	}
	fallback, err := readResults(path, 5, 100, false)
	if err != nil || len(fallback) != 2 {
		t.Fatalf("fallback CSV: %v %v", fallback, err)
	}
	best := filepath.Join(dir, "bestip.txt")
	os.WriteFile(best, []byte("1.1.1.1\n8.8.8.8\n8.8.8.8\n"), 0600)
	if err = mergeBest(best, fallback); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(best)
	if string(b) != "1.1.1.1\n1.0.0.1\n8.8.8.8\n" {
		t.Fatal(string(b))
	}
}

func TestOptimizationStages(t *testing.T) {
	for _, mode := range []string{"cache", "full", "fallback", "empty", "error"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "bestip.txt"), []byte("1.1.1.1\n"), 0600)
			os.WriteFile(filepath.Join(dir, "ip.txt"), []byte("1.0.0.1\n"), 0600)
			script := `#!/bin/sh
set -eu
input=; output=; limit=0
printf '%s\n' "$*" >> calls
while [ "$#" -gt 0 ]; do
 case "$1" in -f) input="$2"; shift 2;; -o) output="$2"; shift 2;; -tl) limit=1; shift 2;; *) shift;; esac
done
printf 'IP,Sent,Recv,Loss,Latency,Speed\n' > "$output"
mode=` + mode + `
case "$mode" in
 cache) printf '1.1.1.1,4,4,0,20,8\n' >> "$output";;
 full) if [ "$input" != "` + filepath.Join(dir, "bestip.txt") + `" ]; then printf '1.0.0.1,4,4,0,30,9\n1.0.0.1,4,4,0,30,9\n' >> "$output"; fi;;
 fallback) if [ "$limit" = 0 ]; then printf '1.0.0.1,4,4,0,999,9\n' >> "$output"; fi;;
 error) printf '1.1.1.1,4,4,0,20,8\n' >> "$output"; exit 1;;
esac
`
			cfst := filepath.Join(dir, "cfst")
			os.WriteFile(cfst, []byte(script), 0700)
			c := config{MinSpeed: 5, MaxLatency: 100, DownloadNumber: 10, DownloadURL: defaultURL, DataDir: dir, CFST: cfst, IPFile: filepath.Join(dir, "ip.txt"), CFSTTimeout: time.Second}
			r, err := optimize(context.Background(), c, true)
			if mode == "empty" || mode == "error" {
				if err == nil {
					t.Fatal("expected no-result error")
				}
			} else if err != nil {
				t.Fatal(err)
			} else if r.IP == "" {
				t.Fatal("empty result")
			}
			b, _ := os.ReadFile(filepath.Join(dir, "calls"))
			calls := strings.Split(strings.TrimSpace(string(b)), "\n")
			want := 3
			if mode == "cache" {
				want = 1
			}
			if mode == "full" {
				want = 2
			}
			if len(calls) != want {
				t.Fatalf("calls: %s", b)
			}
			if strings.Contains(string(b), "allip") {
				t.Fatal("allip should not be enabled")
			}
			if _, err = os.Stat(filepath.Join(dir, "result.csv")); !os.IsNotExist(err) {
				t.Fatal("result.csv was not removed")
			}
			if mode == "full" {
				cache, _ := os.ReadFile(filepath.Join(dir, "bestip.txt"))
				if string(cache) != "1.0.0.1\n1.1.1.1\n" {
					t.Fatal(string(cache))
				}
			}
		})
	}
}
func TestCancellation(t *testing.T) {
	dir := t.TempDir()
	cfst := filepath.Join(dir, "cfst")
	os.WriteFile(cfst, []byte("#!/bin/sh\nexec sleep 30\n"), 0700)
	c := config{DataDir: dir, CFST: cfst, IPFile: "ip.txt", CFSTTimeout: time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := optimize(ctx, c, true)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatal("cfst did not stop on cancellation")
	}
}
func TestScheduling(t *testing.T) {
	cases := []struct{ now, want string }{{"2026-10-10T02:59:59+08:00", "2026-10-10T03:00:00+08:00"}, {"2026-10-10T03:00:00+08:00", "2026-10-10T15:00:00+08:00"}, {"2026-10-10T15:00:00+08:00", "2026-10-11T03:00:00+08:00"}, {"2026-12-31T23:00:00+08:00", "2027-01-01T03:00:00+08:00"}}
	for _, tc := range cases {
		now, _ := time.Parse(time.RFC3339, tc.now)
		if got := nextSchedule(now.UTC()).Format(time.RFC3339); got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.now, got, tc.want)
		}
	}
}
func TestDNSParser(t *testing.T) {
	q, _ := dnsQuery("cf.example.com")
	b := append([]byte(nil), q...)
	binary.BigEndian.PutUint16(b[2:4], 0x8180)
	binary.BigEndian.PutUint16(b[6:8], 1)
	b = append(b, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 1, 1, 1, 1)
	ips, err := parseDNS(b)
	if err != nil || !reflect.DeepEqual(ips, []string{"1.1.1.1"}) {
		t.Fatalf("%v %v", ips, err)
	}
	for i := 0; i < len(b); i++ {
		if _, err = parseDNS(b[:i]); err == nil {
			t.Fatalf("truncated response %d accepted", i)
		}
	}
	b[3] = 0x83
	if _, err = parseDNS(b); err == nil {
		t.Fatal("NXDOMAIN accepted")
	}
	if sameIPs([]string{"1.1.1.1", "1.0.0.1"}, "1.1.1.1") || sameIPs(nil, "1.1.1.1") {
		t.Fatal("mixed/empty DNS accepted")
	}
}
func TestAliyunUpdates(t *testing.T) {
	for _, mode := range []string{"create", "update", "same", "disabled", "cname", "denied"} {
		t.Run(mode, func(t *testing.T) {
			actions := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					t.Error("RPC must use POST")
				}
				r.ParseForm()
				p := r.PostForm
				sig := p.Get("Signature")
				p.Del("Signature")
				if sig != signature(p, "test-secret") {
					t.Error("invalid signature")
				}
				action := p.Get("Action")
				actions = append(actions, action)
				w.Header().Set("Content-Type", "application/json")
				switch action {
				case "GetMainDomainName":
					fmt.Fprint(w, `{"DomainName":"example.com","RR":"cf"}`)
				case "DescribeDomainRecords":
					rows := []record{}
					if mode == "cname" {
						rows = append(rows, record{RR: "cf", Type: "CNAME"})
					} else if mode != "create" && mode != "denied" {
						value := "1.0.0.1"
						if mode == "same" || mode == "disabled" {
							value = "1.1.1.1"
						}
						status := "ENABLE"
						if mode == "disabled" {
							status = "DISABLE"
						}
						rows = append(rows, record{RecordID: "123", RR: "cf", Type: "A", Value: value, TTL: 600, Line: "default", Status: status})
					}
					json.NewEncoder(w).Encode(map[string]any{"TotalCount": len(rows), "DomainRecords": map[string]any{"Record": rows}})
				case "AddDomainRecord", "UpdateDomainRecord":
					if p.Get("Value") != "1.1.1.1" || p.Get("RR") != "cf" {
						t.Error("invalid record")
					}
					if mode == "denied" {
						fmt.Fprint(w, `{"Code":"Forbidden","Message":"test-secret","RequestId":"request"}`)
					} else {
						fmt.Fprint(w, `{"RecordId":"123"}`)
					}
				case "SetDomainRecordStatus":
					if p.Get("Status") != "Enable" {
						t.Error("record not enabled")
					}
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected %s", action)
				}
			}))
			defer server.Close()
			a := aliyun{config{Domain: "cf.example.com", KeyID: "test-id", Secret: "test-secret", TTL: 600}, server.URL, server.Client()}
			err := a.update(context.Background(), "1.1.1.1")
			if mode == "cname" || mode == "denied" {
				if err == nil {
					t.Fatal("expected error")
				}
				if strings.Contains(err.Error(), "test-secret") {
					t.Fatal("secret leaked")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			expected := []string{"GetMainDomainName", "DescribeDomainRecords"}
			switch mode {
			case "create", "denied":
				expected = append(expected, "AddDomainRecord")
			case "update":
				expected = append(expected, "UpdateDomainRecord")
			case "disabled":
				expected = append(expected, "SetDomainRecordStatus")
			}
			if !reflect.DeepEqual(actions, expected) {
				t.Fatalf("%v", actions)
			}
		})
	}
}
func TestSigningKnownVector(t *testing.T) {
	p := url.Values{"Action": {"DescribeRegions"}, "AccessKeyId": {"testid"}, "Format": {"XML"}, "SignatureMethod": {"HMAC-SHA1"}, "SignatureNonce": {"3ee8c1b8-xxxx-xxxx-xxxx-xxxxxxxxxxxx"}, "SignatureVersion": {"1.0"}, "Timestamp": {"2014-05-26T12:00:00Z"}, "Version": {"2014-05-26"}}
	// Independent vector produced from canonical RPC string, not URL form ordering.
	if percent("a b~+/:") != "a%20b~%2B%2F%3A" {
		t.Fatal("RPC percent escaping incorrect")
	}
	if got := signature(p, "testsecret"); got != "ztNxqUPm6VNbRmYVXQ7LNnnRHcM=" {
		t.Fatalf("known signature vector: %s", got)
	}
}

func TestScheduledOptimizationSkipsCache(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%v", fallback), func(t *testing.T) {
			dir := t.TempDir()
			best := filepath.Join(dir, "bestip.txt")
			os.WriteFile(best, []byte("1.1.1.1\n"), 0600)
			input := filepath.Join(dir, "ip.txt")
			os.WriteFile(input, []byte("1.0.0.1\n"), 0600)
			script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> calls
input=; output=; limit=0
while [ "$#" -gt 0 ]; do
 case "$1" in -f) input="$2"; shift 2;; -o) output="$2"; shift 2;; -tl) limit=1; shift 2;; *) shift;; esac
done
# A valid cache result would be returned if the cache stage ran accidentally.
printf 'IP,Sent,Recv,Loss,Latency,Speed\n' > "$output"
if [ "$input" = "` + best + `" ]; then
 printf '1.1.1.1,4,4,0,20,100\n' >> "$output"
elif [ "` + strconv.FormatBool(fallback) + `" = false ]; then
 printf '1.0.0.1,4,4,0,30,50\n' >> "$output"
elif [ "$limit" = 0 ]; then
 printf '1.0.0.1,4,4,0,999,50\n' >> "$output"
fi
`
			cfst := filepath.Join(dir, "cfst")
			os.WriteFile(cfst, []byte(script), 0700)
			c := config{MinSpeed: 5, MaxLatency: 100, DownloadNumber: 7, DownloadURL: "https://example.com/custom.bin", CFST: cfst, IPFile: input, DataDir: dir, CFSTTimeout: time.Second}
			result, err := optimize(context.Background(), c, false)
			if err != nil || result.IP != "1.0.0.1" {
				t.Fatalf("scheduled optimization: %v %v", result, err)
			}
			b, _ := os.ReadFile(filepath.Join(dir, "calls"))
			calls := strings.Split(strings.TrimSpace(string(b)), "\n")
			want := 1
			if fallback {
				want = 2
			}
			if len(calls) != want || strings.Contains(string(b), best) {
				t.Fatalf("unexpected stages: %s", b)
			}
			for _, call := range calls {
				if !strings.Contains(call, "-dn 7") || !strings.Contains(call, "-url https://example.com/custom.bin") {
					t.Fatalf("download settings not forwarded: %s", call)
				}
			}
			cache, _ := os.ReadFile(best)
			if !fallback && string(cache) != "1.0.0.1\n1.1.1.1\n" {
				t.Fatalf("scheduled full result not merged: %s", cache)
			}
		})
	}
}
