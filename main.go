package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"io"

	"bufio"
	"strconv"

	ss "github.com/shadowsocks/shadowsocks-go/shadowsocks"
	yaml "gopkg.in/yaml.v2"
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
	name      string
	rt        int32
	startTime time.Time
}

type dataRow struct {
	timestamp int64
	columns   map[string]int32
}

const (
	indexFile       = "index.html"
	defaultCheckURL = "http://connectivitycheck.gstatic.com/generate_204"
)

var (
	cfg         = Config{}
	namesList   = []string{}
	namesSet    = map[string]bool{}
	baseDirPath string
	baseDirFile *os.File
	rows        = []dataRow{}
)

func makeTimestamp() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

func convertSsURL(s string) (string, error) {
	if !strings.Contains(s, "@") {
		originalURL := s
		parts := strings.SplitAfterN(s, "//", 2)
		if len(parts) < 2 {
			return s, errors.New("invalid url")
		}
		decoded, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return s, err
		}
		s = parts[0] + string(decoded)
		log.Printf("converted %s -> %s", originalURL, s)
	}
	return s, nil
}

func testOne(strURL string) (rt int32, err error) {
	ssURL, err := url.Parse(strURL)
	if err != nil {
		return -1, err
	}
	method := ssURL.User.Username()
	password, ok := ssURL.User.Password()
	if !ok {
		return -1, errors.New("no password")
	}
	cipher, err := ss.NewCipher(method, password)
	if err != nil {
		return -1, err
	}

	conn, err := net.DialTimeout("tcp", ssURL.Host, 5*time.Second)
	if err != nil {
		return -1, err
	}
	defer conn.Close()
	c := ss.NewConn(conn, cipher)

	tr := &http.Transport{
		Dial: func(network, addr string) (net.Conn, error) {
			rawAddr, err := ss.RawAddr(addr)
			if err != nil {
				return nil, err
			}
			if _, err = c.Write(rawAddr); err != nil {
				c.Close()
				return nil, err
			}
			return c, nil
		},
		ResponseHeaderTimeout: 10 * time.Second,
	}
	req, err := http.NewRequest("GET", cfg.CheckURL, nil)
	if err != nil {
		return -1, err
	}
	startTime := makeTimestamp()
	resp, err := tr.RoundTrip(req)
	rt = int32(makeTimestamp() - startTime)
	if err != nil {
		return rt, err
	}
	if resp.StatusCode != 204 {
		return rt, fmt.Errorf("return %d %s but not 204", resp.StatusCode, resp.Status)
	}
	return rt, nil
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
		panic(err)
	}
	if err := yaml.Unmarshal([]byte(b), &cfg); err != nil {
		panic(err)
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
	for i := 0; i < len(cfg.Sites); i++ {
		site := &cfg.Sites[i]
		site.Name = strings.TrimSpace(site.Name)
		if site.Name == "" {
			log.Fatal("name must be specified")
		}
		if _, ok := namesSet[site.Name]; ok {
			log.Fatal("name must be unique")
		}
		namesSet[site.Name] = true
		namesList = append(namesList, site.Name)
		site.Url, err = convertSsURL(strings.TrimSpace(site.Url))
		if err != nil {
			log.Fatalf("url: %s error: %v", site.Url, err)
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

func renderIndexTmp() (error) {
	path := filepath.Join(baseDirPath, indexFile + ".tmp")
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
	data.Names = namesList
	data.GeneratedTime = time.Now().Format("2006-01-02 15:04:05")
	for _, row := range rows {
		rts := []int32{}
		for _, name := range namesList {
			rt, ok := row.columns[name]
			if !ok {
				rt = 0
			}
			rts = append(rts, rt)
		}
		timestamp := time.Unix(row.timestamp, 0).Format("2006-01-02 15:04")
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
		rows[i].columns[result.name] = result.rt
	} else {
		rows = insertSlices(rows, i, dataRow{rowTimestamp, map[string]int32{result.name: result.rt}})
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
			line := fmt.Sprintf("%d,%s,%d\n", result.startTime.Unix(), result.name, result.rt)
			if _, err := f.WriteString(line); err != nil {
				panic(err)
			}
			i := insertResultIntoRows(result)
			if len(rows[i].columns) == len(namesList) {
				f, err = rotateDataFile(f)
				if err != nil {
					panic(err)
				}
				renderIndex()
			}
		}
	}()

	go func() {
		for {
			checkStart := time.Now()
			for _, site := range cfg.Sites {
				go func(site SiteConfig) {
					log.Printf("testing %s", site.Name)
					var err error
					var rt int32
					for retry := 1; retry <= 3; retry++ {
						retryStart := time.Now().Unix()
						rt, err = testOne(site.Url)
						if err != nil {
							remain := time.Duration(15 - (time.Now().Unix() - retryStart))
							log.Printf("#%d %s rt: %d ms, error: %v, sleep %ds", retry, site.Name, rt, err, remain)
							rt = -1
							if remain > 0 {
								time.Sleep(remain * time.Second)
							}
						} else {
							log.Printf("#%d %s rt: %d ms", retry, site.Name, rt)
							break
						}
					}
					resultChan <- benchmarkResult{site.Name, rt, checkStart}
				}(site)
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
		defer f.Close()
		reader := bufio.NewReader(f)
		for {
			bytes, _, err := reader.ReadLine()
			if err != nil {
				if err != io.EOF {
					log.Printf("read %s error: %v", path, err)
				}
				break
			}
			line := string(bytes)
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
			name := line[firstIdx+1 : secondIdx]
			if _, ok := namesSet[name]; !ok {
				log.Printf("name %s not exist in config.yaml", name)
				continue
			}
			rtStr := line[secondIdx+1:]
			rt, err := strconv.ParseInt(rtStr, 10, 0)
			if err != nil {
				log.Printf("strconv rt %s error: %v", rtStr, err)
				continue
			}
			result := benchmarkResult{name, int32(rt), time.Unix(int64(timestamp), 0)}
			insertResultIntoRows(result)
		}
		now = now.AddDate(0, 0, -1)
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
	var err error
	readConfig()
	log.Printf("base dir: %s", baseDirPath)
	log.Printf("oldest history in minutes: %d", cfg.OldestHistory)
	baseDirFile, err = os.Open(baseDirPath)
	if err != nil {
		panic(err)
	}
	defer func() {
		baseDirFile.Sync()
		baseDirFile.Close()
	}()
	loadFiles()
	startCheckers()
	startHTTPServer()
}
