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
	"strings"
	"time"

	ss "github.com/shadowsocks/shadowsocks-go/shadowsocks"
	yaml "gopkg.in/yaml.v2"
)

const (
	checkURL = "http://connectivitycheck.gstatic.com/generate_204"
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

	tr := &http.Transport{
		Dial: func(network, addr string) (net.Conn, error) {
			rawAddr, err := ss.RawAddr(addr)
			if err != nil {
				return nil, err
			}
			return ss.DialWithRawAddr(rawAddr, ssURL.Host, cipher)
		},
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

type SiteConfig struct {
	Name string `yaml:"name"`
	Url  string `yaml:"url"`
}

type Config struct {
	Sites []SiteConfig `yaml:"sites"`
}

func readConfig() *Config {
	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		log.Fatal(err)
	}

	cfg := Config{}
	path := filepath.Join(dir, "config.yaml")
	if stat, err := os.Stat(path); err != nil || stat.IsDir() {
		wd, err := os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
		path = filepath.Join(wd, "config.yaml")
	}
	b, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(b), &cfg); err != nil {
		log.Fatal(err)
	}
	return &cfg
}

func startCheckers(cfg *Config) {
	go func() {
		for {
			for _, site := range cfg.Sites {
				go func(site SiteConfig) {
					log.Printf("testing %s", site.Name)
					rt, err := testOne(site.Url)
					log.Printf("%s rt: %d ms, error: %v", site.Name, rt, err)
				}(site)
			}
			time.Sleep(1 * time.Minute)
		}

	}()
}

func main() {
	cfg := readConfig()
	var err error
	for i := 0; i < len(cfg.Sites); i++ {
		site := &cfg.Sites[i]
		site.Url, err = convertSsURL(site.Url)
		if err != nil {
			log.Fatalf("url: %s error: %v", site.Url, err)
		}
	}
	log.Printf("config: %vv", cfg)
	startCheckers(cfg)

}
