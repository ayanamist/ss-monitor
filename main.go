package main

import (
	"encoding/csv"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emirpasic/gods/v2/maps/treemap"
	"github.com/go-resty/resty/v2"
	"gopkg.in/yaml.v2"
)

type SiteGroup struct {
	Name        string `yaml:"name"`
	ClashApi    string `yaml:"clash_api"`
	ClashSecret string `yaml:"clash_secret"`
	Provider    string `yaml:"provider"`
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
	key       providerKey
	name      string
	rt        int32
	startTime time.Time
}

type providerKey struct {
	ClashApi string
	Provider string
}

type dataRow struct {
	timestamp int64
	columns   map[string]int32 // map[name]rt
}

type empty struct{}

const (
	indexFile = "index.html"

	defaultCheckURL = "https://i.ytimg.com/generate_204"
)

var (
	globalConfig = &Config{}

	baseDirPath string
	baseDirFile *os.File

	globalDataRows   = make(map[providerKey]*treemap.Map[int64, map[string]int32]) // map[providerKey]map[timestamp]map[name]rt
	globalProxyNames = make(map[providerKey][]string)
	globalDataMutex  = &sync.RWMutex{}

	globalDialer = &net.Dialer{
		Timeout: 5 * time.Second,
	}

	globalRestyClient = resty.New().SetDisableWarn(true).SetTimeout(15 * time.Second).SetTransport(&http.Transport{
		DialContext:       globalDialer.DialContext,
		DisableKeepAlives: true,
	}).SetRetryCount(3).SetRetryWaitTime(200 * time.Millisecond).AddRetryCondition(func(response *resty.Response, err error) bool {
		return err != nil
	})
)

func readConfig() {
	var err error
	baseDirPath, err = os.Getwd()
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
	b, err := os.ReadFile(path)
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
		if group.Name == "" {
			log.Fatalf("group name must be specified")
		}
		if api := strings.TrimSuffix(group.ClashApi, "/"); api == "" {
			log.Fatalf("clash_api for group %s must be specified", group.Name)
		} else {
			group.ClashApi = api
		}
		if group.Provider == "" {
			log.Fatalf("provider for group %s must be specified", group.Name)
		}
		globalDataRows[providerKey{
			ClashApi: group.ClashApi,
			Provider: group.Provider,
		}] = treemap.New[int64, map[string]int32]()
	}
}

var resultChan = make(chan benchmarkResult)

func dataFileName(t time.Time) string {
	return fmt.Sprintf("data.%s.csv", t.Local().Format("2006-01-02"))
}

func rotateDataFile(oldFile *os.File) *os.File {
	newFileName := dataFileName(time.Now())
	if oldFile != nil {
		if filepath.Base(oldFile.Name()) == newFileName {
			return oldFile
		}
		_ = oldFile.Sync()
		oldFile.Close()
		os.Remove(dataFileName(time.Now().AddDate(0, 0, -2)))
	}
	newPath := filepath.Join(baseDirPath, newFileName)
	f, err := os.OpenFile(newPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
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
	data.GeneratedTime = time.Now().Local().Format("01-02 15:04:05")
	globalDataMutex.RLock()
	for _, group := range globalConfig.SiteGroups {
		l := globalProxyNames[providerKey{ClashApi: group.ClashApi, Provider: group.Provider}]
		serverNames := make([]string, len(l))
		copy(serverNames, l)
		var rows []struct {
			Time   string
			RtList []int32
		}
		dataRows := globalDataRows[providerKey{ClashApi: group.ClashApi, Provider: group.Provider}]
		it := dataRows.Iterator()
		it.End()
		for it.Prev() {
			timestamp := it.Key()
			row := it.Value()
			if len(row) == 0 {
				continue // skip empty rows
			}
			timestampStr := time.Unix(timestamp, 0).Local().Format("01-02 15:04")
			rts := make([]int32, len(serverNames))
			for i, name := range serverNames {
				rts[i] = row[name]
			}
			rows = append(rows, struct {
				Time   string
				RtList []int32
			}{Time: timestampStr, RtList: rts})
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
			Rows:        rows,
		})
	}
	globalDataMutex.RUnlock()

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
	globalDataMutex.Lock()
	defer globalDataMutex.Unlock()
	dataRows := globalDataRows[result.key]
	rowTimestamp := result.startTime.Truncate(time.Minute).Unix()
	if minKey, _, ok := dataRows.Min(); ok && minKey > rowTimestamp {
		return false // skip if the row is too old
	}
	row, ok := dataRows.Get(rowTimestamp)
	if !ok {
		row = make(map[string]int32)
		dataRows.Put(rowTimestamp, row)
		for dataRows.Size() >= globalConfig.OldestHistory {
			if minKey, _, ok := dataRows.Min(); ok {
				dataRows.Remove(minKey)
			} else {
				break
			}
		}
	}

	row[result.name] = result.rt

	if names := globalProxyNames[result.key]; len(names) > 0 {
		for _, k := range names {
			if _, ok := row[k]; !ok {
				return false
			}
		}
	}
	return true
}

func startCheckers() {
	go func() {
		f := rotateDataFile(nil)
		defer f.Close()
		csvWriter := csv.NewWriter(f)
		for result := range resultChan {
			if err := csvWriter.Write([]string{strconv.FormatInt(result.startTime.Unix(), 10), strconv.FormatInt(int64(result.rt), 10), result.key.ClashApi, result.key.Provider, result.name}); err != nil {
				log.Printf("csv write error: %v", err)
			} else {
				csvWriter.Flush()
			}
			if insertResultIntoRows(result) {
				f = rotateDataFile(f)
				renderIndex()
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(time.Minute)
		for range ticker.C {
			checkStart := time.Now()
			for _, group := range globalConfig.SiteGroups {
				go func(group *SiteGroup) {
					var proxiesResponse struct {
						Proxies []struct {
							Name string `json:"name"`
						} `json:"proxies"`
					}
					r := globalRestyClient.R().ExpectContentType("application/json").SetResult(&proxiesResponse)
					if group.ClashSecret != "" {
						r.SetHeader("Authorization", "Bearer "+group.ClashSecret)
					}
					log.Printf("group=%s get proxies", group.Name)
					resp, err := r.Get(group.ClashApi + "/providers/proxies/" + url.PathEscape(group.Provider))
					if err != nil {
						log.Printf("group=%s get proxies error: %v", group.Name, err)
						return
					}
					if resp.IsError() {
						log.Printf("group=%s get proxies error: %s", group.Name, resp)
						return
					}
					if len(proxiesResponse.Proxies) == 0 {
						log.Printf("group=%s no proxies found for provider %s: %s", group.Name, group.Provider, resp)
						return
					}
					proxyNames := make([]string, len(proxiesResponse.Proxies))
					for i, proxy := range proxiesResponse.Proxies {
						proxyNames[i] = proxy.Name
					}
					key := providerKey{ClashApi: group.ClashApi, Provider: group.Provider}
					globalDataMutex.Lock()
					globalProxyNames[key] = proxyNames
					globalDataMutex.Unlock()
					for _, name := range proxyNames {
						go func(key providerKey, name string) {
							var delayResponse struct {
								Delay *int32 `json:"delay"`
							}
							r := globalRestyClient.R().ExpectContentType("application/json").SetResult(&delayResponse)
							if group.ClashSecret != "" {
								r.SetHeader("Authorization", "Bearer "+group.ClashSecret)
							}
							log.Printf("group=%s get delay for %s ", group.Name, name)
							resp, err := r.SetQueryParams(map[string]string{
								"url":     globalConfig.CheckURL,
								"timeout": "14000", // 14 seconds
							}).Get(group.ClashApi + "/proxies/" + url.PathEscape(name) + "/delay")
							delay := int32(-1)
							if err == nil && delayResponse.Delay != nil {
								delay = *delayResponse.Delay
							} else {
								log.Printf("group=%s get delay for %s error: %v %v", group.Name, name, err, resp)
							}
							resultChan <- benchmarkResult{key, name, delay, checkStart}
						}(key, name)
					}
				}(group)
			}
		}
	}()
}

func loadFiles() {
	now := time.Now()
	minLen := func() int {
		n := math.MaxInt
		for _, l := range globalDataRows {
			if l.Size() < n {
				n = l.Size()
			}
		}
		return n
	}
	for minLen() < globalConfig.OldestHistory {
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
			csvReader := csv.NewReader(f)
			csvReader.FieldsPerRecord = 5 // timestamp, rt, clash_url, provider, proxyName
			for {
				records, err := csvReader.Read()
				if err != nil {
					if err != io.EOF {
						log.Printf("csv read %s error: %v", f.Name(), err)
					}
					break
				}
				timestamp, err := strconv.ParseInt(records[0], 10, 0)
				if err != nil {
					log.Printf("strconv timestamp %s error: %v", records[0], err)
					continue
				}
				rt, err := strconv.ParseInt(records[1], 10, 0)
				if err != nil {
					log.Printf("strconv rt %s error: %v", records[2], err)
					continue
				}
				result := benchmarkResult{key: providerKey{ClashApi: records[2], Provider: records[3]}, name: records[4], rt: int32(rt), startTime: time.Unix(timestamp, 0)}
				insertResultIntoRows(result)
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
