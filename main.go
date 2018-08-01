package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/net/proxy"
	"gopkg.in/yaml.v2"
)

type SiteConfig struct {
	Name string `yaml:"name"`
	Url  string `yaml:"url"`
}

type Config struct {
	HttpPort      string       `yaml:"http_port"`
	OldestHistory int          `yaml:"oldest_history"`
	SlowThreshold int32        `yaml:"slow_threshold"`
	ShowRT        bool         `yaml:"show_rt"`
	CheckURL      string       `yaml:"check_url"`
	Sites         []SiteConfig `yaml:"sites"`
}

type benchmarkResult struct {
	hash      string
	rt        int32
	startTime time.Time
}

type dataRow struct {
	timestamp int64
	columns   map[string]int32
}

const (
	indexFile       = "index.html"
	defaultCheckURL = "http://www.google.com/generate_204"
)

type empty struct {
}

var (
	cfg = Config{}

	servers        []Server
	serverNames    []string
	serversHashSet = make(map[string]empty)

	baseDirPath string
	baseDirFile *os.File

	rows []dataRow

	globalDialer = &net.Dialer{
		Timeout: 5 * time.Second,
	}

	ssrLocalIp = binary.BigEndian.Uint32([]byte(net.ParseIP("127.0.1.0").To4()))
)

type Server interface {
	Test() (rt int32, err error)
	Hash() string
	Name() string
}

type SsrServer struct {
	Server        string `json:"server"`
	ServerPort    int    `json:"server_port"`
	Method        string `json:"method"`
	Password      string `json:"password"`
	Protocol      string `json:"protocol"`
	ProtocolParam string `json:"protocol_param"`
	Obfs          string `json:"obfs"`
	ObfsParam     string `json:"obfs_param"`
	LocalAddr     string `json:"local_address"`
	LocalPort     int    `json:"local_port"`

	name string `json:"-"`
	hash string `json:"-"`

	cmd *exec.Cmd `json:"-"`
}

func (s *SsrServer) restartProcess() error {
	if s.cmd != nil {
		s.cmd.Process.Kill()
		if err := s.cmd.Wait(); err != nil {
			if eer, ok := err.(*exec.ExitError); ok {
				if status, ok := eer.Sys().(syscall.WaitStatus); ok {
					if status.Signaled() {
						switch status.Signal() {
						case syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT:
							err = nil
						}
					}
				}
			}
			if err != nil {
				log.Printf("%s exit: %v", s.Hash(), err)
			}
		}
		s.cmd = nil
	}

	l, err := net.Listen("tcp", s.LocalAddr+":0")
	if err != nil {
		return errors.Wrap(err, "try local port")
	}
	_, portStr, _ := net.SplitHostPort(l.Addr().String())
	l.Close()

	s.LocalPort, _ = strconv.Atoi(portStr)

	confBytes, _ := json.Marshal(s)
	f, err := ioutil.TempFile("", "ssr_config")
	if err != nil {
		return errors.Wrap(err, "open temp file")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(confBytes); err != nil {
		return errors.Wrap(err, "write temp file")
	}

	cmd := exec.Command("sslocal", "-c", f.Name())
	stdoutPr, err := cmd.StdoutPipe()
	if err != nil {
		return errors.Wrap(err, "cmd.StdoutPipe")
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "start sslocal")
	}

	go func() {
		scanner := bufio.NewScanner(stdoutPr)
		for scanner.Scan() {
			log.Printf("%s:%d stdout: %s", s.Server, s.ServerPort, scanner.Text())
		}
	}()

	addr := fmt.Sprintf("%s:%d", s.LocalAddr, s.LocalPort)

	var isListening bool
	for i := 0; i < 50; i++ {
		c, err := globalDialer.Dial("tcp4", addr)
		if err == nil {
			c.Close()
			isListening = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !isListening {
		return errors.Errorf("%s not listening %s", s.Hash(), addr)
	}
	s.cmd = cmd
	return nil
}

func (s *SsrServer) Test() (rt int32, err error) {
	dialer, _ := proxy.SOCKS5("tcp4", fmt.Sprintf("%s:%d", s.LocalAddr, s.LocalPort), nil, globalDialer)
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
		ResponseHeaderTimeout: 10 * time.Second,
	}
	req, err := http.NewRequest("GET", cfg.CheckURL, nil)
	if err != nil {
		return -1, errors.Wrap(err, "new http request")
	}
	startTime := time.Now()
	resp, err := tr.RoundTrip(req)
	rt = int32(time.Now().Sub(startTime) / time.Millisecond)
	if resp != nil {
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "connection refused") {
			if err := s.restartProcess(); err != nil {
				log.Printf("%s restartProcess: %v", s.Hash(), err)
			}
		}
		return rt, errors.Wrapf(err, "roundtrip")
	}
	return rt, nil
}

func (s *SsrServer) Hash() string {
	return s.hash
}

func (s *SsrServer) Name() string {
	return s.name
}

func newServerFromURL(name, rawurl string) (Server, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, errors.Wrap(err, "parse url "+rawurl)
	}
	s := &SsrServer{}
	switch u.Scheme {
	case "ss":
		host, portStr, err := net.SplitHostPort(u.Host)
		if err != nil {
			return nil, errors.Wrap(err, "split host port "+u.Host)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, errors.Wrap(err, "port invalid "+portStr)
		}
		s.Server = host
		s.ServerPort = port
		if u.User == nil {
			return nil, errors.Errorf("empty method password")
		}
		s.Method = u.User.Username()
		s.Password, _ = u.User.Password()
		s.Obfs = "plain"
		s.Protocol = "origin"
	case "ssr":
		splitted := strings.Split(u.Host, ":")
		if len(splitted) != 6 {
			return nil, errors.Errorf("invalid ssr host")
		}
		s.Server = splitted[0]
		s.ServerPort, err = strconv.Atoi(splitted[1])
		if err != nil {
			return nil, errors.Wrap(err, "port invalid "+splitted[1])
		}
		s.Protocol = splitted[2]
		s.Method = splitted[3]
		s.Obfs = splitted[4]
		s.Password, err = b64SafeDecode(splitted[5])
		if err != nil {
			return nil, errors.Wrapf(err, "invalid base64pass: %s", splitted[5])
		}
		s.ObfsParam, _ = b64SafeDecode(u.Query().Get("obfsparam"))
		s.ProtocolParam, _ = b64SafeDecode(u.Query().Get("protoparam"))
	default:
		return nil, errors.Errorf("unsupported scheme %s: %s", u.Scheme, rawurl)
	}
	s.name = name
	s.hash = fmt.Sprintf("%s:%d", s.Server, s.ServerPort)
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, atomic.AddUint32(&ssrLocalIp, 1))
	s.LocalAddr = net.IP(buf).String()
	return s, nil
}

func convertBase64URL(s string) string {
	originalURL := s
	parts := strings.SplitAfterN(s, "//", 2)
	if len(parts) < 2 {
		return s
	}
	decoded, err := b64SafeDecode(parts[1])
	if err != nil {
		return s
	}
	s = parts[0] + decoded
	log.Printf("converted %s -> %s", originalURL, s)
	return s
}

func b64SafeDecode(s string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return string(b), err
}

func readConfig() {
	var err error
	baseDirPath, err = filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		panic(err)
	}

	path := filepath.Join(baseDirPath, "config.yaml")
	if stat, err := os.Stat(path); err != nil || stat.IsDir() {
		baseDirPath, err = os.Getwd()
		if err != nil {
			panic(err)
		}
		path = filepath.Join(baseDirPath, "config.yaml")
	}
	b, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatalf("read %s: %v", path, err)
	}
	if err := yaml.Unmarshal([]byte(b), &cfg); err != nil {
		log.Fatalf("parse json: %v", err)
	}
	cfg.HttpPort = strings.TrimSpace(cfg.HttpPort)
	if cfg.HttpPort == "" {
		log.Fatal("http_port must be specified")
	}
	if cfg.OldestHistory <= 0 {
		cfg.OldestHistory = 60
	}
	if cfg.SlowThreshold <= 0 {
		cfg.SlowThreshold = 5000
	}
	cfg.CheckURL = strings.TrimSpace(cfg.CheckURL)
	if cfg.CheckURL == "" {
		cfg.CheckURL = defaultCheckURL
	}
	namesSet := make(map[string]empty)
	for i := 0; i < len(cfg.Sites); i++ {
		site := &cfg.Sites[i]
		site.Name = strings.TrimSpace(site.Name)
		if site.Name == "" {
			log.Fatalf("name must be specified: %#v", site)
		}
		if _, ok := namesSet[site.Name]; ok {
			log.Fatalf("name %s must be unique", site.Name)
		}
		namesSet[site.Name] = empty{}
		urlStr := convertBase64URL(strings.TrimSpace(site.Url))
		server, err := newServerFromURL(site.Name, urlStr)
		if err != nil {
			log.Fatalf("new server error %s: %v", urlStr, err)
		}
		hash := server.Hash()
		if _, ok := serversHashSet[hash]; ok {
			log.Fatalf("site %s hash must be unique", site.Name)
		}
		serversHashSet[hash] = empty{}
		servers = append(servers, server)
		serverNames = append(serverNames, site.Name)
	}
}

func dropTimeSecond(t time.Time) time.Time {
	return time.Unix(t.Unix()-int64(t.Second()), 0)
}

var resultChan = make(chan benchmarkResult)

func dataFileName(t time.Time) string {
	return fmt.Sprintf("data.%s.csv", t.Format("2006-01-02"))
}

func rotateDataFile(oldFile *os.File) (*os.File, error) {
	newFileName := dataFileName(time.Now())
	if oldFile != nil {
		oldFile.Sync()
		if filepath.Base(oldFile.Name()) == newFileName {
			return oldFile, nil
		}
		oldFile.Close()
	}
	f, err := os.OpenFile(filepath.Join(baseDirPath, newFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY|os.O_SYNC, 0600)
	if err != nil {
		return nil, err
	}
	log.Printf("rotate to %s", newFileName)
	baseDirFile.Sync()
	return f, nil

}

func renderIndex() {
	if err := renderIndexTmp(); err != nil {
		log.Printf("FATAL: %v", err)
		return
	}
	newpath := filepath.Join(baseDirPath, indexFile)
	oldpath := newpath + ".tmp"
	if err := os.Rename(oldpath, newpath); err != nil {
		log.Printf("FATAL: rotate index file: %v", err)
		return
	}
	log.Print("render index complete")
}

func renderIndexTmp() error {
	path := filepath.Join(baseDirPath, indexFile+".tmp")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Printf("open %s error: %v", path, err)
		return err
	}
	defer f.Close()
	data := struct {
		Names []string
		Rows  []struct {
			Time   string
			RtList []int32
		}
		GeneratedTime string
	}{}
	data.Names = serverNames
	data.GeneratedTime = time.Now().Format("01-02 15:04:05")
	for _, row := range rows {
		var rts []int32
		for _, server := range servers {
			rt, ok := row.columns[server.Hash()]
			if !ok {
				rt = 0
			}
			rts = append(rts, rt)
		}
		timestamp := time.Unix(row.timestamp, 0).Format("01-02 15:04")
		data.Rows = append(data.Rows, struct {
			Time   string
			RtList []int32
		}{timestamp, rts})
	}
	tplFile := indexFile + ".tpl"
	tpl, err := template.New(tplFile).Funcs(map[string]interface{}{
		"isRtSlow": func(rt int32) bool {
			return rt >= cfg.SlowThreshold
		},
		"renderRt": func(rt int32) string {
			if rt == 0 {
				return "-"
			}
			if cfg.ShowRT {
				return strconv.FormatInt(int64(rt), 10)
			}
			if rt < 0 {
				return "ERROR"
			}
			return "OK"
		},
	}).ParseFiles(filepath.Join(baseDirPath, tplFile))
	if err != nil {
		return fmt.Errorf("template parse: %v", err)
	}
	if err := tpl.Execute(f, data); err != nil {
		return fmt.Errorf("template execute: %v", err)
	}
	return nil
}

func insertSlices(rows []dataRow, i int, row dataRow) []dataRow {
	return append(rows[:i], append([]dataRow{row}, rows[i:]...)...)
}

func insertResultIntoRows(result benchmarkResult) int {
	rowTimestamp := dropTimeSecond(result.startTime).Unix()
	i := sort.Search(len(rows), func(i int) bool {
		return rowTimestamp >= rows[i].timestamp
	})
	if len(rows) > 0 && i < len(rows) && rows[i].timestamp == rowTimestamp {
		rows[i].columns[result.hash] = result.rt
	} else {
		rows = insertSlices(rows, i, dataRow{rowTimestamp, map[string]int32{result.hash: result.rt}})
		if len(rows) > cfg.OldestHistory {
			rows = rows[:cfg.OldestHistory]
			if i >= cfg.OldestHistory {
				i = cfg.OldestHistory - 1
			}
		}
	}
	return i
}

func startCheckers() {
	go func() {
		var err error

		f, err := rotateDataFile(nil)
		defer func() {
			if f != nil {
				f.Close()
			}
		}()
		for result := range resultChan {
			line := fmt.Sprintf("%d,%s,%d\n", result.startTime.Unix(), result.hash, result.rt)
			if _, err := f.WriteString(line); err != nil {
				log.Println(err)
				continue
			}
			i := insertResultIntoRows(result)
			if len(rows[i].columns) == len(serverNames) {
				f, err = rotateDataFile(f)
				if err != nil {
					log.Println(err)
				}
				renderIndex()
			}
		}
	}()

	go func() {
		for {
			checkStart := time.Now()
			for _, server := range servers {
				go func(server Server) {
					log.Printf("testing %s", server.Hash())
					var err error
					var rt int32
					for retry := 1; retry <= 3; retry++ {
						retryStart := time.Now().Unix()
						rt, err = server.Test()
						if err != nil {
							remain := time.Duration(15 - (time.Now().Unix() - retryStart))
							log.Printf("#%d %s rt: %d ms, error: %v, sleep %ds", retry, server.Hash(), rt, err, remain)
							rt = -1
							if remain > 0 {
								time.Sleep(remain * time.Second)
							}
						} else {
							log.Printf("#%d %s rt: %d ms", retry, server.Hash(), rt)
							break
						}
					}
					resultChan <- benchmarkResult{server.Hash(), rt, checkStart}
				}(server)
			}
			time.Sleep(1 * time.Minute)
		}

	}()
}

func loadFiles() {
	now := time.Now()
	for len(rows) < cfg.OldestHistory {
		path := filepath.Join(baseDirPath, dataFileName(now))
		if stat, err := os.Stat(path); err != nil || stat.IsDir() {
			log.Printf("file %s not exist", path)
			break
		}
		f, err := os.Open(path)
		if err != nil {
			log.Printf("open %s error: %v", path, err)
			break
		}
		func() {
			defer f.Close()
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := scanner.Text()
				firstIdx := strings.Index(line, ",")
				if firstIdx < 0 {
					log.Printf("line %s cant find first comma", line)
					continue
				}
				tsStr := line[:firstIdx]
				timestamp, err := strconv.ParseUint(tsStr, 10, 0)
				if err != nil {
					log.Printf("strconv timestamp %s error: %v", tsStr, err)
					continue
				}
				secondIdx := strings.LastIndex(line, ",")
				if secondIdx == firstIdx {
					log.Printf("line %s cant find last comma", line)
					continue
				}
				hash := line[firstIdx+1 : secondIdx]
				if _, ok := serversHashSet[hash]; !ok {
					log.Printf("hash %s not exist in config.yaml", hash)
					continue
				}
				rtStr := line[secondIdx+1:]
				rt, err := strconv.ParseInt(rtStr, 10, 0)
				if err != nil {
					log.Printf("strconv rt %s error: %v", rtStr, err)
					continue
				}
				result := benchmarkResult{hash, int32(rt), time.Unix(int64(timestamp), 0)}
				insertResultIntoRows(result)
			}
			if err := scanner.Err(); err != nil {
				log.Printf("scan %s: %v", f.Name(), err)
			}
			now = now.AddDate(0, 0, -1)
		}()
	}
}

func startHTTPServer() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(filepath.Join(baseDirPath, indexFile))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("not found"))
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		stat, err := f.Stat()
		if err == nil {
			w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
		}
		io.Copy(w, f)
	})
	if !strings.Contains(cfg.HttpPort, ":") {
		cfg.HttpPort = ":" + cfg.HttpPort
	}
	server := &http.Server{Addr: cfg.HttpPort, Handler: nil}
	ln, err := net.Listen("tcp", cfg.HttpPort)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listen on %s", ln.Addr().String())
	if err := server.Serve(ln); err != nil {
		log.Fatal(err)
	}
}

func main() {
	runtime.GOMAXPROCS(1)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	var err error
	readConfig()
	log.Printf("base dir: %s", baseDirPath)
	log.Printf("oldest history in minutes: %d", cfg.OldestHistory)
	baseDirFile, err = os.Open(baseDirPath)
	if err != nil {
		log.Fatalf("open %s: %v", baseDirPath, err)
	}
	defer func() {
		baseDirFile.Sync()
		baseDirFile.Close()
	}()
	loadFiles()
	startCheckers()
	startHTTPServer()
}
