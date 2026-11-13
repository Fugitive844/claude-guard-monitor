package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const version = "1.2.9"

type GuardConfig struct {
	DNSIP       string   `json:"dns_ip"`
	DNSPort     int      `json:"dns_port"`
	DNSSources  []string `json:"dns_sources"`
	DNSPorts    []int    `json:"dns_ports"`
	ClientCIDRs []string `json:"client_cidrs"`
	RouterIPs   []string `json:"router_ips"`
}
type Config struct {
	Listen       string      `json:"listen"`
	DataDir      string      `json:"data_dir"`
	PasswordHash string      `json:"password_hash"`
	AllowedCIDRs []string    `json:"allowed_cidrs"`
	Group        uint16      `json:"nflog_group"`
	LogBackend   string      `json:"log_backend,omitempty"`
	DetailDays   int         `json:"detail_days"`
	StatsDays    int         `json:"stats_days"`
	MaxDBMB      int64       `json:"max_db_mb"`
	Guard        GuardConfig `json:"guard"`
}

func defaults() Config {
	return Config{Listen: "192.168.10.1:9088", DataDir: "/overlay/claude-guard-monitor", AllowedCIDRs: []string{"127.0.0.1/32", "::1/128", "192.168.10.0/24", "192.168.20.0/24"}, Group: 10631, DetailDays: 7, StatsDays: 30, MaxDBMB: 100, Guard: GuardConfig{DNSIP: "127.0.0.1", DNSPort: 15353, DNSSources: []string{"127.0.0.1", "::1", "192.168.10.1", "192.168.20.1"}, DNSPorts: []int{53, 11400, 15353, 15355}, ClientCIDRs: []string{"192.168.10.0/24", "192.168.20.0/24", "fd00:1234:5678::/48"}, RouterIPs: []string{"192.168.10.1", "192.168.20.1"}}}
}
func randomToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func passwordHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	t := path + ".new"
	if e := os.WriteFile(t, b, mode); e != nil {
		return e
	}
	return os.Rename(t, path)
}
func saveConfig(path string, c Config) error {
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	return writeAtomic(path, b, 0600)
}
func validGuard(g GuardConfig) error {
	if _, e := netip.ParseAddr(g.DNSIP); e != nil {
		return fmt.Errorf("DNS IP 无效")
	}
	if g.DNSPort < 1 || g.DNSPort > 65535 {
		return fmt.Errorf("DNS 端口无效")
	}
	if len(g.DNSSources) == 0 || len(g.DNSPorts) == 0 || len(g.ClientCIDRs) == 0 || len(g.RouterIPs) == 0 {
		return fmt.Errorf("地址和端口列表不能为空")
	}
	if len(g.DNSSources) > 32 || len(g.DNSPorts) > 16 || len(g.ClientCIDRs) > 32 || len(g.RouterIPs) > 32 {
		return fmt.Errorf("列表过长")
	}
	for _, s := range append(append([]string{}, g.DNSSources...), g.RouterIPs...) {
		if _, e := netip.ParseAddr(s); e != nil {
			return fmt.Errorf("IP 无效: %s", s)
		}
	}
	for _, s := range g.ClientCIDRs {
		p, e := netip.ParsePrefix(s)
		if e != nil || p.Bits() < 8 {
			return fmt.Errorf("客户端网段无效或过宽: %s", s)
		}
	}
	for _, p := range g.DNSPorts {
		if p < 1 || p > 65535 {
			return fmt.Errorf("DNS 端口无效")
		}
	}
	return nil
}
func ensureCertificate(dir, listen string) error {
	cert := filepath.Join(dir, "server.crt")
	key := filepath.Join(dir, "server.key")
	if _, e := os.Stat(cert); e == nil {
		if _, e = os.Stat(key); e == nil {
			return nil
		}
	}
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return e
	}
	host, _, e := net.SplitHostPort(listen)
	if e != nil {
		return e
	}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if ip := net.ParseIP(host); ip != nil {
		ips = append(ips, ip)
	}
	t := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Claude Guard Local Monitor"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(2, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}, IPAddresses: ips}
	der, e := x509.CreateCertificate(rand.Reader, &t, &t, &k.PublicKey, k)
	if e != nil {
		return e
	}
	kb, e := x509.MarshalPKCS8PrivateKey(k)
	if e != nil {
		return e
	}
	if e = writeAtomic(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0600); e != nil {
		return e
	}
	return writeAtomic(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
}

type session struct {
	Expiry time.Time
	CSRF   string
}
type App struct {
	cfg            Config
	configPath     string
	demo           bool
	db             *Store
	mu             sync.RWMutex
	snapshot       Snapshot
	lastError      string
	collectorState string
	logBackend     string
	received       uint64
	lost           uint64
	writerLost     uint64
	sessions       map[string]session
	loginAttempts  map[string]time.Time
	lastLoginSweep time.Time
	events         chan Event
	ctx            context.Context
	lifeMu         sync.Mutex
	op             string
	speed          *SpeedService
}

func main() {
	path := flag.String("config", "/etc/claude-guard-monitor/config.json", "config path")
	initMode := flag.Bool("init", false, "initialize configuration and credentials only")
	demo := flag.Bool("demo", false, "loopback-only demo without system operations")
	listen := flag.String("listen", "", "listen override")
	allow := flag.String("allow-cidrs", "", "management CIDRs for initialization, comma separated")
	show := flag.Bool("version", false, "print version")
	flag.Parse()
	if *show {
		fmt.Println(version)
		return
	}
	c := defaults()
	if *initMode {
		if _, e := os.Stat(*path); e == nil {
			log.Fatal("configuration exists; refusing overwrite")
		}
		if *listen != "" {
			c.Listen = *listen
		}
		if *allow != "" {
			c.AllowedCIDRs = strings.Split(*allow, ",")
			for i, cidr := range c.AllowedCIDRs {
				cidr = strings.TrimSpace(cidr)
				if _, err := netip.ParsePrefix(cidr); err != nil {
					log.Fatal("invalid management CIDR: ", cidr)
				}
				c.AllowedCIDRs[i] = cidr
			}
		}
		if *demo {
			c.DataDir = filepath.Join(filepath.Dir(*path), "data")
			c.Listen = "127.0.0.1:19088"
		}
		pw := randomToken()
		c.PasswordHash = passwordHash(pw)
		if e := saveConfig(*path, c); e != nil {
			log.Fatal(e)
		}
		credentials := "用户名: admin\n密码: " + pw + "\n地址: https://" + c.Listen + "\n"
		if *demo {
			credentials = "用户名: admin\n密码: " + pw + "\n地址: http://" + c.Listen + "\n"
		}
		if e := writeAtomic(filepath.Join(filepath.Dir(*path), "credentials.txt"), []byte(credentials), 0600); e != nil {
			log.Fatal(e)
		}
		fmt.Println("Initialized config and private credentials.txt; password not printed.")
		return
	}
	b, e := os.ReadFile(*path)
	if e != nil {
		log.Fatal(e)
	}
	if e = json.Unmarshal(b, &c); e != nil {
		log.Fatal(e)
	}
	if *listen != "" {
		c.Listen = *listen
	}
	if len(c.PasswordHash) != 64 {
		log.Fatal("invalid password hash")
	}
	if e = validGuard(c.Guard); e != nil {
		log.Fatal(e)
	}
	if c.DetailDays < 1 || c.StatsDays < 1 || c.MaxDBMB < 16 || c.Group == 0 {
		log.Fatal("invalid retention/group config")
	}
	if c.LogBackend != "" && c.LogBackend != "auto" && c.LogBackend != "nflog" && c.LogBackend != "syslog" {
		log.Fatal("invalid log backend")
	}
	for _, s := range c.AllowedCIDRs {
		if _, e = netip.ParsePrefix(s); e != nil {
			log.Fatal(e)
		}
	}
	host, _, e := net.SplitHostPort(c.Listen)
	if e != nil {
		log.Fatal(e)
	}
	ip, e := netip.ParseAddr(host)
	if e != nil || ip.IsUnspecified() {
		log.Fatal("listen must bind a specific IP")
	}
	if *demo && !ip.IsLoopback() {
		log.Fatal("demo must bind loopback")
	}
	if e = os.MkdirAll(c.DataDir, 0700); e != nil {
		log.Fatal(e)
	}
	db, e := openStore(filepath.Join(c.DataDir, "monitor.db"))
	if e != nil {
		log.Fatal(e)
	}
	defer db.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a := &App{cfg: c, configPath: *path, demo: *demo, db: db, sessions: map[string]session{}, loginAttempts: map[string]time.Time{}, events: make(chan Event, 4096), ctx: ctx, collectorState: "starting"}
	a.speed = newSpeedService(a)
	a.logBackend = selectLogBackend(c.LogBackend)
	if !*demo {
		if e = ensureCertificate(filepath.Dir(*path), c.Listen); e != nil {
			log.Fatal(e)
		}
	}
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); a.writer() }()
	go a.poll()
	go a.collect()
	a.audit("monitor_start", "监控服务启动 "+version)
	e = a.serve()
	cancel()
	a.speed.wait()
	<-writerDone
	if e != nil {
		log.Print(e)
	}
}
func constantEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
