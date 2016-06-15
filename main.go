package main

import (
	"encoding/base64"
	"errors"
	"fmt"
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

	ss "github.com/shadowsocks/shadowsocks-go/shadowsocks"
	yaml "gopkg.in/yaml.v2"
)

type SiteConfig struct {
	Name string `yaml:"name"`
	Url  string `yaml:"url"`
}

type Config struct {
	Sites []SiteConfig `yaml:"sites"`
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
	checkURL = "http://connectivitycheck.gstatic.com/generate_204"
	htmlTpl  = `
<html>
<header>
<title>System Status</title>
<meta charset="UTF-8"/>
<style>
body {
	font-family: monospace;
}
</style>
</header>
<body>
<pre>
%s
</pre>
</body>
</html>
	`
)

var (
	rows        = []dataRow{}
	baseDirPath string
	baseDirFile *os.File
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
	req, err := http.NewRequest("GET", checkURL, nil)
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

func readConfig() *Config {
	var err error
	baseDirPath, err = filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		panic(err)
	}

	cfg := Config{}
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
	return &cfg
}

func dropTimeSecond(t time.Time) time.Time {
	return time.Unix(t.Unix()-int64(t.Second()), 0)
}

var resultChan = make(chan benchmarkResult)

func rotateDataFile(oldFile *os.File) (*os.File, error) {
	newFileName := fmt.Sprintf("data.%s.csv", time.Now().Format("2006-01-02"))
	if oldFile.Name() == newFileName {
		return oldFile, nil
	}
	oldFile.Sync()
	oldFile.Close()
	f, err := os.OpenFile(filepath.Join(baseDirPath, newFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	log.Printf("rotate to %s", newFileName)
	baseDirFile.Sync()
	return f, nil

}

func render() {
	// TODO
}

func startCheckers(cfg *Config) {
	names := make([]string, len(cfg.Sites))
	for i, site := range cfg.Sites {
		names[i] = site.Name
	}
	go func() {
		var err error
		var f *os.File
		defer func() {
			if f != nil {
				f.Close()
			}
		}()
		for {
			result := <-resultChan
			line := fmt.Sprintf("%d,%s,%d", result.startTime.Unix(), result.name, result.rt)
			if _, err := f.WriteString(line); err != nil {
				panic(err)
			}

			rowTimestamp := dropTimeSecond(result.startTime).Unix()
			i := sort.Search(len(rows), func(i int) bool {
				return rowTimestamp <= rows[i].timestamp
			})
			if rows[i].timestamp == rowTimestamp {
				rows[i].columns[result.name] = result.rt
			} else {
				rows = append(rows[:i], append([]dataRow{dataRow{rowTimestamp, map[string]int32{result.name: result.rt}}}, rows[i:]...)...)
			}
			if len(rows[i].columns) == len(names) {
				f, err = rotateDataFile(f)
				if err != nil {
					panic(err)
				}
				render()
			}
		}
	}()

	go func() {
		for {
			startTime := time.Now()
			for _, site := range cfg.Sites {
				go func(site SiteConfig) {
					log.Printf("testing %s", site.Name)
					rt, err := testOne(site.Url)
					log.Printf("%s rt: %d ms, error: %v", site.Name, rt, err)
					resultChan <- benchmarkResult{site.Name, rt, startTime}
				}(site)
			}
			time.Sleep(1 * time.Minute)
		}

	}()
}

func startHTTPServer() {
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

func main() {
	var err error
	cfg := readConfig()
	baseDirFile, err = os.Open(baseDirPath)
	if err != nil {
		panic(err)
	}
	defer func() {
		baseDirFile.Sync()
		baseDirFile.Close()
	}()
	for i := 0; i < len(cfg.Sites); i++ {
		site := &cfg.Sites[i]
		site.Url, err = convertSsURL(site.Url)
		if err != nil {
			log.Fatalf("url: %s error: %v", site.Url, err)
		}
	}
	log.Printf("config: %vv", cfg)
	startCheckers(cfg)
	startHTTPServer()
}
