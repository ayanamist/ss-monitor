package main

import (
	"bufio"
	"container/list"
	"context"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	ss "github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	"gopkg.in/yaml.v2"
)

type SiteGroup struct {
	Name    string          `yaml:"name"`
	Servers []*ServerConfig `yaml:"servers"`
}

type ServerConfig struct {
	Name    string `yaml:"name"`
	Url     string `yaml:"url"`
	server  Server
	group   *SiteGroup
	results []benchmarkResult
}

type Config struct {
	HttpPort      string       `yaml:"http_port"`
	OldestHistory int          `yaml:"oldest_history"`
	SlowThreshold int32        `yaml:"slow_threshold"`
	ShowRT        bool         `yaml:"show_rt"`
	CheckURL      string       `yaml:"check_url"`
	SiteGroups    []*SiteGroup `yaml:"groups"`
}

type benchmarkResult struct {
	hash      string
	rt        int32
	startTime time.Time
}

type dataRow struct {
	timestamp int64
	columns   map[string]int32 // map[serverHash]rt
}

const (
	indexFile       = "index.html"
	defaultCheckURL = "http://www.google.com/generate_204"
)

type empty struct {
}

var (
	globalConfig = &Config{}

	serversByHash = make(map[string]*ServerConfig)

	baseDirPath string
	baseDirFile *os.File

	globalDataRows = list.New()

	globalDialer = &net.Dialer{
		Timeout: 5 * time.Second,
	}
)

type Server interface {
	Test() (rt int32, err error)
	Hash() string
	Name() string
}

type SsServer struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Method     string `json:"method"`
	Password   string `json:"password"`

	name   string
	hash   string
	cipher ss.Cipher
}

func (s *SsServer) Test() (rt int32, err error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := globalDialer.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", s.Server, s.ServerPort))
			if err != nil {
				return nil, errors.Wrap(err, "globalDialer.DialContext")
			}
			c = s.cipher.StreamConn(c)
			rawaddr := socks.ParseAddr(addr)
			if len(rawaddr) == 0 {
				c.Close()
				return nil, errors.Errorf("invalid addr %s", addr)
			}
			if _, err := c.Write(rawaddr); err != nil {
				c.Close()
				return nil, errors.Wrap(err, "c.Write")
			}
			return c, nil
		},
		ResponseHeaderTimeout: 10 * time.Second,
	}
	req, err := http.NewRequest(http.MethodHead, globalConfig.CheckURL, nil)
	if err != nil {
		return -1, errors.Wrap(err, "new http request")
	}
	startTime := time.Now()
	resp, err := tr.RoundTrip(req)
	rt = int32(time.Now().Sub(startTime) / time.Millisecond)
	if resp != nil {
		_, _ = io.Copy(ioutil.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if err != nil {
		return rt, errors.Wrapf(err, "roundtrip")
	}
	return rt, nil
}

func (s *SsServer) Hash() string {
	return s.hash
}

func (s *SsServer) Name() string {
	return s.name
}

func newServerFromURL(name, rawurl string) (_ Server, err error) {
	s := &SsServer{}
	if strings.HasPrefix(rawurl, "ss://") {
		u, err := url.Parse(rawurl)
		if err != nil {
			return nil, err
		}
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
		s.cipher, err = ss.PickCipher(s.Method, nil, s.Password)
		if err != nil {
			return nil, errors.Wrap(err, "ss.PickCipher")
		}
	} else {
		return nil, errors.Errorf("unsupported scheme %s", rawurl)
	}
	s.name = name
	s.hash = fmt.Sprintf("%s:%d", s.Server, s.ServerPort)
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
	if s == "" {
		return "", nil
	}
	var decodeFunc func(s string) ([]byte, error)
	if s[len(s)-1] == byte(base64.StdPadding) {
		decodeFunc = base64.URLEncoding.DecodeString
	} else {
		decodeFunc = base64.RawURLEncoding.DecodeString
	}
	b, err := decodeFunc(s)
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
	if err := yaml.Unmarshal(b, &globalConfig); err != nil {
		log.Fatalf("parse json: %v", err)
	}
	globalConfig.HttpPort = strings.TrimSpace(globalConfig.HttpPort)
	if globalConfig.HttpPort == "" {
		log.Fatal("http_port must be specified")
	}
	if globalConfig.OldestHistory <= 0 {
		globalConfig.OldestHistory = 60
	}
	if globalConfig.SlowThreshold <= 0 {
		globalConfig.SlowThreshold = 5000
	}
	globalConfig.CheckURL = strings.TrimSpace(globalConfig.CheckURL)
	if globalConfig.CheckURL == "" {
		globalConfig.CheckURL = defaultCheckURL
	}
	for _, group := range globalConfig.SiteGroups {
		group.Name = strings.TrimSpace(group.Name)
		if group.Name == "" {
			log.Fatalf("group name must be specified: %#v", group)
		}
		for _, serverConfig := range group.Servers {
			namesSet := make(map[string]empty)
			serverConfig.Name = strings.TrimSpace(serverConfig.Name)
			if serverConfig.Name == "" {
				log.Fatalf("server name must be specified: %#v", serverConfig)
			}
			if _, ok := namesSet[serverConfig.Name]; ok {
				log.Fatalf("server name %s must be group unique", serverConfig.Name)
			}
			namesSet[serverConfig.Name] = empty{}
			urlStr := convertBase64URL(strings.TrimSpace(serverConfig.Url))
			server, err := newServerFromURL(serverConfig.Name, urlStr)
			if err != nil {
				log.Fatalf("new serverConfig error %s: %v", urlStr, err)
			}
			hash := server.Hash()
			if _, ok := serversByHash[hash]; ok {
				log.Fatalf("server %s hash must be global unique", serverConfig.Name)
			}
			tmpConfig := serverConfig
			tmpConfig.server = server
			tmpConfig.group = &SiteGroup{
				Name: group.Name,
			}
			serversByHash[hash] = tmpConfig
		}
	}
}

func dropTimeSecond(t time.Time) time.Time {
	return time.Unix(t.Unix()-int64(t.Second()), 0)
}

var resultChan = make(chan benchmarkResult)

func dataFileName(t time.Time) string {
	return fmt.Sprintf("data.%s.csv", t.Format("2006-01-02"))
}

func rotateDataFile(oldFile *os.File) *os.File {
	newFileName := dataFileName(time.Now())
	if oldFile != nil {
		if filepath.Base(oldFile.Name()) == newFileName {
			return oldFile
		}
		_ = oldFile.Sync()
		oldFile.Close()
	}
	newPath := filepath.Join(baseDirPath, newFileName)
	f, err := os.OpenFile(newPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatalf("rotateDataFile to %s error: %s", newPath, err)
	}
	log.Printf("rotate to %s", newFileName)
	_ = baseDirFile.Sync()
	return f

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
		Groups []struct {
			Name        string
			ServerNames []string
			Rows        []struct {
				Time   string
				RtList []int32
			}
		}
		GeneratedTime string
	}{}
	data.GeneratedTime = time.Now().Format("01-02 15:04:05")
	for _, group := range globalConfig.SiteGroups {
		serverNames := make([]string, len(group.Servers))
		for i, v := range group.Servers {
			serverNames[i] = v.Name
		}
		data.Groups = append(data.Groups, struct {
			Name        string
			ServerNames []string
			Rows        []struct {
				Time   string
				RtList []int32
			}
		}{
			Name:        group.Name,
			ServerNames: serverNames,
		})
	}
	for e := globalDataRows.Front(); e != nil; e = e.Next() {
		row := e.Value.(*dataRow)
		timestamp := time.Unix(row.timestamp, 0).Format("01-02 15:04")
		for i, group := range globalConfig.SiteGroups {
			rts := make([]int32, len(group.Servers))
			for j, serverConfig := range group.Servers {
				rt := row.columns[serverConfig.server.Hash()]
				rts[j] = rt
			}
			data.Groups[i].Rows = append(data.Groups[i].Rows, struct {
				Time   string
				RtList []int32
			}{Time: timestamp, RtList: rts})
		}
	}
	tplFile := indexFile + ".tpl"
	tpl, err := template.New(tplFile).Funcs(map[string]interface{}{
		"isRtSlow": func(rt int32) bool {
			return rt >= globalConfig.SlowThreshold
		},
		"renderRt": func(rt int32) string {
			if rt == 0 {
				return "-"
			}
			if globalConfig.ShowRT {
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

func insertResultIntoRows(result benchmarkResult) (curRowComplete bool) {
	if _, ok := serversByHash[result.hash]; !ok {
		log.Printf("unknown hash and discard: %#v", result)
		return false
	}

	rowTimestamp := dropTimeSecond(result.startTime).Unix()
	var row *dataRow
	if globalDataRows.Front() == nil {
		row = &dataRow{
			timestamp: rowTimestamp,
			columns:   make(map[string]int32),
		}
		globalDataRows.PushFront(row)
	} else {
		for e := globalDataRows.Front(); e != nil; e = e.Next() {
			tmpRow := e.Value.(*dataRow)
			if tmpRow.timestamp < rowTimestamp {
				row = &dataRow{
					timestamp: rowTimestamp,
					columns:   make(map[string]int32),
				}
				globalDataRows.PushFront(row)
				break
			} else if tmpRow.timestamp == rowTimestamp {
				row = tmpRow
				break
			}
		}
		if row == nil {
			log.Printf("WARN too old data and discard: %#v", result)
			return false
		}
	}
	row.columns[result.hash] = result.rt
	if len(row.columns) < len(serversByHash) {
		return false
	}
	for globalDataRows.Len() >= globalConfig.OldestHistory {
		if e := globalDataRows.Back(); e != nil {
			globalDataRows.Remove(e)
		} else {
			break
		}
	}
	return true
}

func startCheckers() {
	go func() {
		f := rotateDataFile(nil)
		defer f.Close()
		for result := range resultChan {
			line := fmt.Sprintf("%d,%s,%d\n", result.startTime.Unix(), result.hash, result.rt)
			if _, err := f.WriteString(line); err != nil {
				log.Println(err)
				continue
			}
			if insertResultIntoRows(result) {
				f = rotateDataFile(f)
				renderIndex()
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(time.Minute)
		for ; ; <-ticker.C {
			checkStart := time.Now()
			for _, serverConfig := range serversByHash {
				go func(serverConfig *ServerConfig) {
					serverHash := serverConfig.server.Hash()
					groupName := serverConfig.group.Name
					log.Printf("group=%s server=%s start testing", groupName, serverHash)
					var err error
					var rt int32
					interval := time.NewTicker(15 * time.Second)
					for retry := 1; ; retry++ {
						rt, err = serverConfig.server.Test()
						if err != nil {
							log.Printf("group=%s server=%s retry#%d rt: %d ms, error: %v",
								groupName, serverHash, retry, rt, err)
							rt = -1
						} else {
							log.Printf("group=%s server=%s retry#%d rt: %d ms",
								groupName, serverHash, retry, rt)
							break
						}
						if retry >= 3 {
							break
						}
						<-interval.C
					}
					interval.Stop()
					resultChan <- benchmarkResult{serverHash, rt, checkStart}
				}(serverConfig)
			}
		}
	}()
}

func loadFiles() {
	now := time.Now()
	for globalDataRows.Len() < globalConfig.OldestHistory {
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
				line := strings.TrimSpace(scanner.Text())
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
				if _, ok := serversByHash[hash]; !ok {
					log.Printf("line %s hash %s not exist in config.yaml", line, hash)
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
			_, _ = w.Write([]byte("not found"))
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		stat, err := f.Stat()
		if err == nil {
			w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
		}
		_, _ = io.Copy(w, f)
	})
	if !strings.Contains(globalConfig.HttpPort, ":") {
		globalConfig.HttpPort = ":" + globalConfig.HttpPort
	}
	server := &http.Server{Addr: globalConfig.HttpPort, Handler: nil}
	ln, err := net.Listen("tcp", globalConfig.HttpPort)
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
	log.Printf("oldest history in minutes: %d", globalConfig.OldestHistory)
	baseDirFile, err = os.Open(baseDirPath)
	if err != nil {
		log.Fatalf("open %s: %v", baseDirPath, err)
	}
	defer func() {
		_ = baseDirFile.Sync()
		baseDirFile.Close()
	}()
	loadFiles()
	startCheckers()
	startHTTPServer()
}
