package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/PuerkitoBio/goquery"
)

const (
	MangaPrefix   = "http://marumaru.in/b/manga/"
	ArchivePrefix = "http://www.mangaumaru.com/archives/"
	UserAgent     = "Opera/12.02 (Android 4.1; Linux; Opera Mobi/ADR-1111101157; U; en-US) Presto/2.9.201 Version/12.02" // Opera Mobile 12.02
)

var (
	workerID, fetchID uint32
	Cookie            = &http.Cookie{
		Name:  "sucuri_cloudproxy_uuid_0b08a99da",
		Value: "90a09d699fc2833ec55cdb5a0bb7a794",
	}
	Filter = regexp.MustCompile("[\\[\\]\\:\\s<>\\=\\|\\+]").ReplaceAllString
)

func main() {
	maxPics := flag.Int64("max", -1, "다운로드할 최대 이미지 수")
	downloaders := flag.Uint64("dl", 1, "한 번에 다운로드할 만화 수")
	workers := flag.Uint64("worker", 3, "한 만화당 동시에 다운로드할 회차 수")
	fetchers := flag.Uint64("fetch", 3, "회차당 동시에 다운로드할 이미지 수")
	flag.Parse()
	mangas := flag.Args()

	targets := make(chan uint64, len(mangas))
	for _, manga := range mangas {
		m, err := strconv.ParseUint(manga, 10, 64)
		if err != nil {
			log.Fatal(err)
		}
		targets <- m
	}

	if len(mangas) == 0 {
		fmt.Println("Please input space-seperated manga ID.")
		fmt.Println("Manga ID: http://marumaru.in/b/manga/{ID}")
		os.Exit(1)
	}

	if *fetchers == 0 || *workers == 0 || *maxPics == 0 || *downloaders == 0 {
		fmt.Println("Invalid argument supplied")
		os.Exit(1)
	}
	for i := uint64(0); i < *downloaders; i++ {
		dl := new(Downloader)
		dl.Init(*workers, *fetchers, *maxPics)
		dl.Start(targets)
	}
	fmt.Scanln()
}

func Get(url string) (string, error) { // From http://stackoverflow.com/questions/11692860/how-can-i-efficiently-download-a-large-file-using-go
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("Request creation error: %s", err.Error())
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := new(http.Client).Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request error: %s", err.Error())
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, resp.Body)
	if err != nil {
		return "", fmt.Errorf("Buffer copy error: %s", err.Error())
	}
	return string(buf.Bytes()), nil
}

type Archive struct {
	ID      uint64
	Subject string
}

func GetArchives(manga uint64) (mangas []Archive, err error) {
	var doc *goquery.Document
	doc, err = goquery.NewDocument(MangaPrefix + strconv.FormatUint(manga, 10))
	if err != nil {
		return
	}
	links := make([]Archive, 0)
	doc.Find("div .content").Children().Find("a").Each(func(i int, s *goquery.Selection) {
		if l, ok := s.Attr("href"); ok && strings.Index(l, ArchivePrefix) != -1 {
			if link, err := strconv.ParseUint(l[strings.Index(l, ArchivePrefix)+len(ArchivePrefix):], 10, 64); err == nil {
				sub := s.Text()
				if sub == "" {
					for _, l := range links {
						if l.ID == link {
							return
						}
					}
					sub = "Untitled"
				}
				links = append(links, Archive{
					ID:      link,
					Subject: sub,
				})
			}
		}
	})
	return links, nil
}

func GetPics(archive uint64) (urls []string, err error) {
	req, err := http.NewRequest("GET", ArchivePrefix+strconv.FormatUint(archive, 10), nil)
	if err != nil {
		return nil, fmt.Errorf("Request creation error: %s", err.Error())
	}
	req.Header.Set("User-Agent", UserAgent)
	req.AddCookie(Cookie)
	resp, err := new(http.Client).Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request error: %s", err.Error())
	}
	defer resp.Body.Close()
	var doc *goquery.Document
	doc, err = goquery.NewDocumentFromResponse(resp)
	if err != nil {
		return
	}
	urls = make([]string, 0)
	doc.Find("div .entry-content").Find("p").Contents().Each(func(i int, s *goquery.Selection) {
		if img, ok := s.Attr("data-lazy-src"); ok {
			urls = append(urls, img)
		}
	})
	return
}

type Downloader struct {
	Workers  []*Worker
	workers  uint64
	fetchers uint64
	maxPics  int64
	target   chan<- Archive
	report   <-chan Status
}

func (d *Downloader) Init(workers, fetchers uint64, maxPics int64) {
	d.workers, d.fetchers, d.maxPics = workers, fetchers, maxPics
	d.Workers = make([]*Worker, workers)
	tc, rc := make(chan Archive, 30), make(chan Status, 30)
	for i := range d.Workers {
		d.Workers[i] = NewWorker(fetchers, tc, rc)
	}
	d.target = tc
	d.report = rc
	go func() {
		for r := range d.report {
			log.Print(r)
		}
	}()
}

func (d *Downloader) Start(mangas <-chan uint64) {
	for manga := range mangas {
		log.Printf("Parsing archive list: %s%d", MangaPrefix, manga)
		links, err := GetArchives(manga)
		if err != nil {
			log.Printf("Archive list parse error: %s", err.Error())
			continue
		}
		for _, link := range links {
			d.target <- link
		}
	}
}

type Worker struct {
	ID       uint32
	Fetchers []*Fetcher
	Targets  <-chan Archive
	Report   chan<- Status
	FRequest chan FetchRequest
	FResult  chan FetchResult
}

func NewWorker(fetchers uint64, tc <-chan Archive, rc chan<- Status) *Worker {
	w := new(Worker)
	w.ID = atomic.AddUint32(&workerID, 1)
	w.Targets, w.Report = tc, rc
	w.Fetchers = make([]*Fetcher, fetchers)
	w.FRequest = make(chan FetchRequest, 30)
	w.FResult = make(chan FetchResult, 30*fetchers)
	for i := range w.Fetchers {
		f := new(Fetcher)
		f.ID = atomic.AddUint32(&fetchID, 1)
		f.Request = w.FRequest
		f.Result = w.FResult
		w.Fetchers[i] = f
		go w.Fetchers[i].fetch()
	}
	go w.Watch()
	go func() {
		for result := range w.FResult {
			if !result.Ok {
				log.Printf("Fetch error: %s", result.Description)
			}
		}
	}()
	return w
}

func (w Worker) Watch() {
	for target := range w.Targets {
		os.MkdirAll(Filter(target.Subject, "_"), os.ModeDir)
		log.Printf("Parsing archive: %s%d", ArchivePrefix, target.ID)
		imgs, err := GetPics(target.ID)
		if err != nil {
			log.Printf("Archive parse error: %s", err.Error())
		}
		log.Printf("Archive parse done for: %s%d", ArchivePrefix, target.ID)
		for i, img := range imgs {
			w.FRequest <- FetchRequest{
				URL:     img,
				FileDir: Filter(target.Subject+"/"+strconv.Itoa(i)+".jpg", "_"),
			}
		}
	}
}

func (w Worker) Download(manga uint64) { //TODO: Parse contents here and fetch them
}

type Fetcher struct {
	ID      uint32
	Request <-chan FetchRequest
	Result  chan<- FetchResult
	Report  chan<- Status
}

func (f Fetcher) fetch() {
	for req := range f.Request {
		log.Printf("Fetch start: %s -> %s", req.URL, req.FileDir)
		func(req FetchRequest) {
			file, err := os.Create(req.FileDir)
			if err != nil {
				f.Result <- FetchResult{
					Ok:          false,
					Description: "Could not create file: " + err.Error(),
				}
				return
			}
			defer file.Close()
			s, err := Get(req.URL)
			if err != nil {
				f.Result <- FetchResult{
					Ok:          false,
					Description: err.Error(),
				}
			}
			n, err := file.WriteString(s)
			/*
				            f.Result <- FetchResult{
								Ok:          true,
								Description: strconv.FormatInt(int64(n), 10) + " bytes were written",
							}
			*/
			log.Printf("Fetch to %s succeeded: %d bytes were written", req.FileDir, n)
		}(req)
	}
}

const (
	FetchStart StatusID = iota
	FetchComplete
	FetchError
	PicFetchStart
	PicFetchComplete
	PicFetchError
	ParseStart
	ParseError
)

type StatusID byte

type Status struct {
	ID   StatusID
	Data string
}

type FetchRequest struct {
	URL     string
	FileDir string
}

type FetchResult struct {
	Ok          bool
	Description string
}
