package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultURL = "https://download.parallels.com/desktop/v17/17.1.1-51537/ParallelsDesktop-17.1.1-51537.dmg"

type config struct {
	MinSpeed, MaxLatency                                      float64
	Domain, KeyID, Secret, DownloadURL, CFST, IPFile, DataDir string
	DownloadNumber, TTL                                       int
	CheckInterval, CFSTTimeout                                time.Duration
	Once, Check, DryRun                                       bool
}

func readEnv(path string) (map[string]string, error) {
	values := make(map[string]string)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf(".env 行缺少等号")
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		values[k] = v
	}
	return values, scanner.Err()
}

func loadConfig() (config, error) {
	var c config
	exe, err := os.Executable()
	if err != nil {
		return c, err
	}
	base := filepath.Dir(exe)
	fs := flag.NewFlagSet("cfip-ddns", flag.ContinueOnError)
	envFile := fs.String("env-file", filepath.Join(base, ".env"), ".env 路径")
	fs.Float64Var(&c.MinSpeed, "min-speed", 0, "下载速度下限 MB/s")
	fs.Float64Var(&c.MaxLatency, "max-latency", 0, "ping / 优选延迟上限 ms")
	fs.StringVar(&c.Domain, "aliyun-domain", "", "完整域名")
	fs.StringVar(&c.KeyID, "aliyun-access-key-id", "", "阿里云 AccessKey ID")
	fs.StringVar(&c.Secret, "aliyun-access-key-secret", "", "阿里云 AccessKey Secret")
	fs.IntVar(&c.DownloadNumber, "download-number", 10, "下载测速数量")
	fs.StringVar(&c.DownloadURL, "download-url", defaultURL, "下载测速 URL")
	fs.StringVar(&c.CFST, "cfst", filepath.Join(base, "cfst"), "cfst 可执行文件")
	fs.StringVar(&c.IPFile, "ip-file", filepath.Join(base, "ip.txt"), "完整 IP 列表")
	fs.StringVar(&c.DataDir, "data-dir", base, "状态目录（默认程序所在目录）")
	fs.DurationVar(&c.CheckInterval, "check-interval", 15*time.Minute, "域名检测间隔")
	fs.DurationVar(&c.CFSTTimeout, "cfst-timeout", 6*time.Hour, "每阶段优选超时")
	fs.IntVar(&c.TTL, "dns-ttl", 600, "DNS TTL 秒")
	fs.BoolVar(&c.Once, "once", false, "执行一次优选并退出")
	fs.BoolVar(&c.Check, "check", false, "检测配置、DoH 和阿里云记录后退出（只读）")
	fs.BoolVar(&c.DryRun, "dry-run", false, "优选但不写入阿里云 DNS")
	if err = fs.Parse(os.Args[1:]); err != nil {
		return c, err
	}
	if fs.NArg() != 0 {
		return c, errors.New("不支持位置参数")
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	env, err := readEnv(*envFile)
	if err != nil {
		return c, err
	}
	get := func(k string) string {
		if v, ok := os.LookupEnv(k); ok {
			return v
		}
		return env[k]
	}
	complete := true
	for _, k := range []string{"min-speed", "max-latency", "aliyun-domain", "aliyun-access-key-id", "aliyun-access-key-secret"} {
		if !visited[k] {
			complete = false
		}
	}
	if !complete {
		if visited["min-speed"] || visited["max-latency"] || visited["aliyun-domain"] || visited["aliyun-access-key-id"] || visited["aliyun-access-key-secret"] {
			log.Print("必填命令行参数不完整，忽略整组五项参数，读取环境配置")
		}
		c.MinSpeed, err = strconv.ParseFloat(get("MIN_SPEED"), 64)
		if err != nil {
			return c, errors.New("MIN_SPEED 必须为数字")
		}
		c.MaxLatency, err = strconv.ParseFloat(get("MAX_LATENCY"), 64)
		if err != nil {
			return c, errors.New("MAX_LATENCY 必须为数字")
		}
		c.Domain, c.KeyID, c.Secret = get("ALIYUN_DOMAIN"), get("ALIYUN_ACCESS_KEY_ID"), get("ALIYUN_ACCESS_KEY_SECRET")
	}
	for _, p := range []struct {
		flag, env string
		dest      *string
	}{{"download-url", "DOWNLOAD_URL", &c.DownloadURL}, {"cfst", "CFST_PATH", &c.CFST}, {"ip-file", "IP_FILE", &c.IPFile}, {"data-dir", "DATA_DIR", &c.DataDir}} {
		if v := get(p.env); v != "" && !visited[p.flag] {
			*p.dest = v
		}
	}
	for _, p := range []struct {
		flag, env string
		dest      *int
	}{{"download-number", "DOWNLOAD_NUMBER", &c.DownloadNumber}, {"dns-ttl", "DNS_TTL", &c.TTL}} {
		if v := get(p.env); v != "" && !visited[p.flag] {
			*p.dest, err = strconv.Atoi(v)
			if err != nil {
				return c, fmt.Errorf("%s 必须为整数", p.env)
			}
		}
	}
	for _, p := range []struct {
		flag, env string
		dest      *time.Duration
	}{{"check-interval", "CHECK_INTERVAL", &c.CheckInterval}, {"cfst-timeout", "CFST_TIMEOUT", &c.CFSTTimeout}} {
		if v := get(p.env); v != "" && !visited[p.flag] {
			*p.dest, err = time.ParseDuration(v)
			if err != nil {
				return c, fmt.Errorf("%s 时间格式无效", p.env)
			}
		}
	}
	c.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.Domain), "."))
	if c.KeyID == "" || c.Secret == "" {
		return c, errors.New("阿里云 ID 和 Secret 必填")
	}
	if len(c.Domain) > 253 || !strings.Contains(c.Domain, ".") {
		return c, errors.New("ALIYUN_DOMAIN 必须为完整域名")
	}
	for _, label := range strings.Split(c.Domain, ".") {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return c, errors.New("域名标签无效")
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return c, errors.New("域名仅支持 ASCII，请使用 punycode")
			}
		}
	}
	if math.IsNaN(c.MinSpeed) || math.IsInf(c.MinSpeed, 0) || c.MinSpeed < 0 || math.IsNaN(c.MaxLatency) || math.IsInf(c.MaxLatency, 0) || c.MaxLatency <= 0 {
		return c, errors.New("速度必须非负，延迟必须大于零")
	}
	if c.DownloadNumber < 1 || c.TTL < 1 || c.CheckInterval <= 0 || c.CFSTTimeout <= 0 {
		return c, errors.New("数量、TTL 和时间间隔必须大于零")
	}
	u, err := url.Parse(c.DownloadURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return c, errors.New("download-url 必须为 HTTP(S) URL")
	}
	for _, p := range []*string{&c.CFST, &c.IPFile, &c.DataDir} {
		*p, err = filepath.Abs(*p)
		if err != nil {
			return c, err
		}
	}
	return c, nil
}

// DNS wire-format A query; bootstrap uses DNS over UDP/TCP at 119.29.29.29.
func dnsQuery(name string) ([]byte, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b[:2]); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint16(b[2:4], 0x0100)
	binary.BigEndian.PutUint16(b[4:6], 1)
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if len(label) < 1 || len(label) > 63 {
			return nil, errors.New("无效 DNS 名称")
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	return append(b, 0, 0, 1, 0, 1), nil
}
func skipName(b []byte, pos int) (int, error) {
	for steps := 0; steps < 128; steps++ {
		if pos >= len(b) {
			break
		}
		n := int(b[pos])
		pos++
		if n == 0 {
			return pos, nil
		}
		if n&0xc0 == 0xc0 {
			if pos >= len(b) {
				break
			}
			return pos + 1, nil
		}
		if n&0xc0 != 0 || pos+n > len(b) {
			break
		}
		pos += n
	}
	return 0, errors.New("DNS 名称损坏")
}
func parseDNS(b []byte) ([]string, error) {
	if len(b) < 12 {
		return nil, errors.New("DNS 响应过短")
	}
	flags := binary.BigEndian.Uint16(b[2:4])
	if flags&0x8000 == 0 || flags&0x0200 != 0 {
		return nil, errors.New("DNS 响应无效或截断")
	}
	if flags&15 != 0 {
		return nil, fmt.Errorf("DNS RCODE=%d", flags&15)
	}
	pos := 12
	for i := 0; i < int(binary.BigEndian.Uint16(b[4:6])); i++ {
		var err error
		pos, err = skipName(b, pos)
		if err != nil {
			return nil, err
		}
		pos += 4
		if pos > len(b) {
			return nil, io.ErrUnexpectedEOF
		}
	}
	ips := []string{}
	for i := 0; i < int(binary.BigEndian.Uint16(b[6:8])); i++ {
		var err error
		pos, err = skipName(b, pos)
		if err != nil {
			return nil, err
		}
		if pos+10 > len(b) {
			return nil, io.ErrUnexpectedEOF
		}
		typ, cls := binary.BigEndian.Uint16(b[pos:]), binary.BigEndian.Uint16(b[pos+2:])
		n := int(binary.BigEndian.Uint16(b[pos+8:]))
		pos += 10
		if pos+n > len(b) {
			return nil, io.ErrUnexpectedEOF
		}
		if typ == 1 && cls == 1 && n == 4 {
			ips = append(ips, net.IP(b[pos:pos+4]).String())
		}
		pos += n
	}
	if len(ips) == 0 {
		return nil, errors.New("DNS 没有 A 记录")
	}
	return ips, nil
}
func bootstrap(ctx context.Context) (string, error) {
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, "119.29.29.29:53")
	}}
	ips, err := resolver.LookupIP(ctx, "ip4", "dns.alidns.com")
	if err != nil {
		return "", fmt.Errorf("bootstrap DNS: %w", err)
	}
	if len(ips) == 0 {
		return "", errors.New("bootstrap 没有 IPv4")
	}
	return ips[0].String(), nil
}
func resolveDoH(ctx context.Context, domain string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ip, err := bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, "443"))
	}, TLSHandshakeTimeout: 8 * time.Second}
	defer tr.CloseIdleConnections()
	q, err := dnsQuery(domain)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://dns.alidns.com/dns-query?dns="+base64.RawURLEncoding.EncodeToString(q), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-message")
	resp, err := (&http.Client{Transport: tr, Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DoH HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil, err
	}
	if len(b) < 2 || b[0] != q[0] || b[1] != q[1] {
		return nil, errors.New("DoH DNS ID 不匹配")
	}
	return parseDNS(b)
}

// AliDNS RPC API 2015-01-09, SignatureVersion 1.0.
type aliyun struct {
	cfg      config
	endpoint string
	client   *http.Client
}

func percent(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
func signature(params url.Values, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, percent(k)+"="+percent(params.Get(k)))
	}
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte("POST&%2F&" + percent(strings.Join(parts, "&"))))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
func (a *aliyun) call(ctx context.Context, action string, p url.Values, out any) error {
	if p == nil {
		p = url.Values{}
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	for k, v := range map[string]string{"Action": action, "Format": "JSON", "Version": "2015-01-09", "AccessKeyId": a.cfg.KeyID, "SignatureMethod": "HMAC-SHA1", "SignatureVersion": "1.0", "SignatureNonce": hex.EncodeToString(nonce), "Timestamp": time.Now().UTC().Format("2006-01-02T15:04:05Z")} {
		p.Set(k, v)
	}
	p.Set("Signature", signature(p, a.cfg.Secret))
	req, err := http.NewRequestWithContext(ctx, "POST", a.endpoint, strings.NewReader(p.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("阿里云 %s 请求失败", action)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	var apiErr struct{ Code, Message, RequestID string }
	if err = json.Unmarshal(b, &apiErr); err != nil {
		return errors.New("阿里云响应不是 JSON")
	}
	if apiErr.Code != "" {
		return fmt.Errorf("阿里云 %s: %s (RequestId=%s)", action, apiErr.Code, apiErr.RequestID)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("阿里云 HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

type record struct {
	RecordID                      string `json:"RecordId"`
	RR, Type, Value, Status, Line string
	TTL                           int
}

func (a *aliyun) records(ctx context.Context) (string, string, []record, error) {
	var domain struct{ DomainName, RR string }
	if err := a.call(ctx, "GetMainDomainName", url.Values{"InputString": {a.cfg.Domain}}, &domain); err != nil {
		return "", "", nil, err
	}
	if domain.DomainName == "" {
		return "", "", nil, errors.New("阿里云未返回主域名")
	}
	if domain.RR == "" {
		domain.RR = "@"
	}
	records := []record{}
	for page := 1; ; page++ {
		var resp struct {
			TotalCount    int
			DomainRecords struct{ Record []record }
		}
		p := url.Values{"DomainName": {domain.DomainName}, "PageNumber": {strconv.Itoa(page)}, "PageSize": {"500"}, "RRKeyWord": {domain.RR}, "SearchMode": {"EXACT"}}
		if err := a.call(ctx, "DescribeDomainRecords", p, &resp); err != nil {
			return "", "", nil, err
		}
		for _, r := range resp.DomainRecords.Record {
			if r.RR == domain.RR {
				records = append(records, r)
			}
		}
		if page*500 >= resp.TotalCount {
			break
		}
	}
	return domain.DomainName, domain.RR, records, nil
}
func (a *aliyun) update(ctx context.Context, ip string) error {
	domain, rr, rows, err := a.records(ctx)
	if err != nil {
		return err
	}
	var targets []record
	for _, r := range rows {
		if r.Type == "CNAME" {
			return errors.New("同名 CNAME 与 A 记录冲突，请先处理 CNAME")
		}
		if r.Type == "A" {
			targets = append(targets, r)
		}
	}
	if len(targets) == 0 {
		var out struct {
			RecordID string `json:"RecordId"`
		}
		err = a.call(ctx, "AddDomainRecord", url.Values{"DomainName": {domain}, "RR": {rr}, "Type": {"A"}, "Value": {ip}, "TTL": {strconv.Itoa(a.cfg.TTL)}, "Line": {"default"}}, &out)
		if err == nil {
			log.Printf("已创建 A 记录 %s -> %s", a.cfg.Domain, ip)
		}
		return err
	}
	for _, r := range targets {
		if r.Value != ip || r.TTL != a.cfg.TTL {
			line := r.Line
			if line == "" {
				line = "default"
			}
			var out struct{}
			if err = a.call(ctx, "UpdateDomainRecord", url.Values{"RecordId": {r.RecordID}, "RR": {rr}, "Type": {"A"}, "Value": {ip}, "TTL": {strconv.Itoa(a.cfg.TTL)}, "Line": {line}}, &out); err != nil {
				return err
			}
			log.Printf("已更新 A 记录 %s -> %s（线路 %s）", a.cfg.Domain, ip, line)
		}
		if r.Status == "DISABLE" {
			var out struct{}
			if err = a.call(ctx, "SetDomainRecordStatus", url.Values{"RecordId": {r.RecordID}, "Status": {"Enable"}}, &out); err != nil {
				return err
			}
		}
	}
	return nil
}

type result struct {
	IP             string
	Latency, Speed float64
}

func readResults(path string, minSpeed, maxLatency float64, limitLatency bool) ([]result, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(header) < 6 {
		return nil, errors.New("cfst CSV 列数不足")
	}
	var results []result
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(row) < 6 {
			return nil, errors.New("cfst CSV 行列数不足")
		}
		ip := net.ParseIP(strings.TrimSpace(row[0]))
		lat, e1 := strconv.ParseFloat(strings.TrimSpace(row[4]), 64)
		speed, e2 := strconv.ParseFloat(strings.TrimSpace(row[5]), 64)
		loss, e3 := strconv.ParseFloat(strings.TrimSpace(row[3]), 64)
		if ip == nil || ip.To4() == nil || e1 != nil || e2 != nil || e3 != nil || math.IsNaN(lat) || math.IsNaN(speed) || math.IsNaN(loss) || math.IsInf(lat, 0) || math.IsInf(speed, 0) || math.IsInf(loss, 0) || lat < 0 || speed < minSpeed || loss < 0 || loss > 0.3 || (limitLatency && lat > maxLatency) {
			continue
		}
		results = append(results, result{ip.String(), lat, speed})
	}
	return results, nil
}
func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func mergeBest(path string, rows []result) error {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	seen := map[string]bool{}
	lines := []string{}
	for _, r := range rows {
		if !seen[r.IP] {
			seen[r.IP] = true
			lines = append(lines, r.IP)
		}
	}
	for _, s := range strings.Fields(string(old)) {
		if ip := net.ParseIP(s); ip != nil && ip.To4() != nil && !seen[ip.String()] {
			seen[ip.String()] = true
			lines = append(lines, ip.String())
		}
	}
	return atomicWrite(path, []byte(strings.Join(lines, "\n")+"\n"))
}
func optimize(ctx context.Context, c config, useCache bool) (result, error) {
	best := filepath.Join(c.DataDir, "bestip.txt")
	output := filepath.Join(c.DataDir, "result.csv")
	stages := []struct {
		name, file string
		limit      bool
	}{{"缓存优选", best, true}, {"完整优选", c.IPFile, true}, {"保底优选", c.IPFile, false}}
	for i, s := range stages {
		if ctx.Err() != nil {
			return result{}, ctx.Err()
		}
		if i == 0 {
			if !useCache {
				continue
			}
			info, err := os.Stat(best)
			if errors.Is(err, os.ErrNotExist) || err == nil && info.Size() == 0 {
				continue
			}
			if err != nil {
				return result{}, err
			}
		}
		if err := os.Remove(output); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result{}, err
		}
		args := []string{"-t", "4", "-dt", "6", "-tlr", "0.3", "-url", c.DownloadURL, "-sl", strconv.FormatFloat(c.MinSpeed, 'f', -1, 64), "-dn", strconv.Itoa(c.DownloadNumber), "-f", s.file, "-o", output}
		if s.limit {
			args = append(args, "-tl", strconv.FormatFloat(c.MaxLatency, 'f', -1, 64))
		}
		stageCtx, cancel := context.WithTimeout(ctx, c.CFSTTimeout)
		cmd := exec.CommandContext(stageCtx, c.CFST, args...)
		cmd.Dir = c.DataDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		log.Printf("开始%s，超时 %s", s.name, c.CFSTTimeout)
		err := cmd.Run()
		stageErr := stageCtx.Err()
		cancel()
		if ctx.Err() != nil {
			os.Remove(output)
			return result{}, ctx.Err()
		}
		if err != nil {
			os.Remove(output)
			if stageErr != nil {
				log.Printf("%s 超时", s.name)
			} else {
				log.Printf("%s 进程失败: %v", s.name, err)
			}
			continue
		}
		rows, err := readResults(output, c.MinSpeed, c.MaxLatency, s.limit)
		removeErr := os.Remove(output)
		if err != nil {
			return result{}, err
		}
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return result{}, removeErr
		}
		if len(rows) == 0 {
			log.Printf("%s没有满足条件的结果", s.name)
			continue
		}
		if i == 1 {
			if err = mergeBest(best, rows); err != nil {
				return result{}, err
			}
		}
		log.Printf("优选结果 %s，延迟 %.2f ms，速度 %.2f MB/s", rows[0].IP, rows[0].Latency, rows[0].Speed)
		return rows[0], nil
	}
	return result{}, errors.New("优选各阶段均无可用结果")
}
func sameIPs(ips []string, best string) bool {
	if len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if ip != best {
			return false
		}
	}
	return true
}
func syncDNS(ctx context.Context, c config, a *aliyun, best string) error {
	ips, err := resolveDoH(ctx, c.Domain)
	if err == nil && sameIPs(ips, best) {
		log.Printf("DoH 记录已是 %s，无需更新", best)
		return nil
	}
	if c.DryRun {
		log.Printf("dry-run: DNS 待更新为 %s", best)
		return nil
	}
	return a.update(ctx, best)
}
func runOptimization(ctx context.Context, c config, a *aliyun, useCache bool) error {
	r, err := optimize(ctx, c, useCache)
	if err != nil {
		return err
	}
	// Persist before updating so failed writes are retried on subsequent checks.
	if !c.DryRun {
		if err = atomicWrite(filepath.Join(c.DataDir, "selected-ip.txt"), []byte(r.IP+"\n")); err != nil {
			return err
		}
	}
	return syncDNS(ctx, c, a, r.IP)
}
func checkDomain(ctx context.Context, c config, a *aliyun) error {
	ips, err := resolveDoH(ctx, c.Domain)
	if err != nil {
		log.Print("域名解析失败，触发优选")
		return runOptimization(ctx, c, a, true)
	}
	for _, ip := range ips {
		if !measure(ctx, ip, c.MaxLatency) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Print("延迟或丢包超标，触发优选")
			return runOptimization(ctx, c, a, true)
		}
	}
	b, err := os.ReadFile(filepath.Join(c.DataDir, "selected-ip.txt"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	best := strings.TrimSpace(string(b))
	if net.ParseIP(best) == nil {
		return errors.New("selected-ip.txt 内容无效")
	}
	if !sameIPs(ips, best) {
		return syncDNS(ctx, c, a, best)
	}
	return nil
}

var utc8 = time.FixedZone("UTC+8", 8*3600)

func nextSchedule(now time.Time) time.Time {
	local := now.In(utc8)
	for _, hour := range []int{3, 15} {
		t := time.Date(local.Year(), local.Month(), local.Day(), hour, 0, 0, 0, utc8)
		if t.After(now) {
			return t
		}
	}
	return time.Date(local.Year(), local.Month(), local.Day()+1, 3, 0, 0, 0, utc8)
}
func service(ctx context.Context, c config, a *aliyun) {
	// Serial scheduler: never run more than one cfst; coalesce missed schedules.
	due := nextSchedule(time.Now())
	nextCheck := time.Now()
	for ctx.Err() == nil {
		now := time.Now()
		if !now.Before(due) {
			log.Print("开始定时完整优选，跳过 bestip 缓存阶段")
			if err := runOptimization(ctx, c, a, false); err != nil && ctx.Err() == nil {
				log.Printf("定时优选失败: %v", err)
			}
			due = nextSchedule(time.Now())
			nextCheck = time.Now().Add(c.CheckInterval)
		} else if !now.Before(nextCheck) {
			if err := checkDomain(ctx, c, a); err != nil && ctx.Err() == nil {
				log.Printf("域名检查失败: %v", err)
			}
			nextCheck = time.Now().Add(c.CheckInterval)
		}
		wake := nextCheck
		if due.Before(wake) {
			wake = due
		}
		timer := time.NewTimer(time.Until(wake))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func run() error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("仅支持 Linux x64")
	}
	c, err := loadConfig()
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	a := &aliyun{c, "https://alidns.aliyuncs.com/", &http.Client{Timeout: 30 * time.Second}}
	if c.Check {
		ips, err := resolveDoH(ctx, c.Domain)
		if err != nil {
			log.Printf("DoH 解析失败: %v", err)
		} else {
			log.Printf("DoH A 记录: %v", ips)
		}
		domain, rr, rows, apiErr := a.records(ctx)
		if apiErr != nil {
			return apiErr
		}
		log.Printf("阿里云配置可用，主域名 %s，RR %s，同名记录 %d 条", domain, rr, len(rows))
		return err
	}
	for _, p := range []string{c.CFST, c.IPFile} {
		if info, err := os.Stat(p); err != nil {
			return fmt.Errorf("读取 %s: %w", p, err)
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("%s 不是文件", p)
		}
	}
	if err = os.MkdirAll(c.DataDir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(c.DataDir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("相同数据目录已有运行中的实例")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	log.Printf("启动：域名 %s，速度下限 %.2f MB/s，延迟上限 %.2f ms", c.Domain, c.MinSpeed, c.MaxLatency)
	if c.Once {
		return runOptimization(ctx, c, a, true)
	}
	log.Printf("检测间隔 %s；下次定时优选 %s", c.CheckInterval, nextSchedule(time.Now()).Format(time.RFC3339))
	service(ctx, c, a)
	return nil
}
func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC)
	if err := run(); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Printf("错误: %v", err)
		os.Exit(1)
	}
}
