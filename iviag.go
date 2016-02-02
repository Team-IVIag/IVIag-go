package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/PuerkitoBio/goquery"
	"github.com/davecgh/go-spew/spew"
)

const (
	MangaPrefix   = "http://marumaru.in/b/manga/"
	ArchivePrefix = "http://www.mangaumaru.com/archives/"
)

var workerID, fetchID uint32

func main() {
	maxPics := flag.Int64("max", -1, "다운로드할 최대 이미지 수")
	workers := flag.Uint64("worker", 5, "한 번에 동시에 다운로드할 만화 수")
	fetchers := flag.Uint64("fetch", 1, "만화당 동시에 다운로드할 이미지 수")
	flag.Parse()
	mangas := flag.Args()

	log.Println(mangas, *maxPics, *workers, *fetchers)

	targets := make([]uint64, len(mangas))
	for i, manga := range mangas {
		m, err := strconv.ParseUint(manga, 10, 64)
		if err != nil {
			log.Fatal(err)
		}
		targets[i] = m
	}

	if len(mangas) == 0 {
		fmt.Println("Please input space-seperated manga ID.")
		fmt.Println("Manga ID: http://marumaru.in/b/manga/{ID}")
		os.Exit(1)
	}

	if *fetchers == 0 || *workers == 0 || *maxPics == 0 {
		fmt.Println("Invalid argument supplied")
		os.Exit(1)
	}

	dl := new(Downloader)
	dl.Init(*workers, *fetchers, *maxPics)
	dl.Start(targets)

	fmt.Scanln()
}

func Get(url string) (string, error) { // From http://stackoverflow.com/questions/11692860/how-can-i-efficiently-download-a-large-file-using-go
	resp, err := http.Get(url)
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

func GetPics(archive uint64) ([]string, error) {
	return nil, nil
}

type Downloader struct {
	Workers  []*Worker
	workers  uint64
	fetchers uint64
	maxPics  int64
	target   chan<- uint64
	report   <-chan Status
}

func (d *Downloader) Init(workers, fetchers uint64, maxPics int64) {
	d.workers, d.fetchers, d.maxPics = workers, fetchers, maxPics
	d.Workers = make([]*Worker, workers)
	tc, rc := make(chan uint64, 30), make(chan Status, 30)
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

func (d *Downloader) Start(mangas []uint64) {
	for _, manga := range mangas {
		d.target <- manga
		spew.Dump(GetArchives(manga))
	}
}

type Worker struct {
	ID       uint32
	Fetchers []*Fetcher
	Targets  <-chan uint64
	Report   chan<- Status
	FRequest chan FetchRequest
	FResult  chan FetchResult
}

func NewWorker(fetchers uint64, tc <-chan uint64, rc chan<- Status) *Worker {
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
	return w
}

func (w Worker) Watch() {
	for target := range w.Targets {
		w.Download(target)
	}
}

func (w Worker) Download(manga uint64) { //TODO: Parse contents here and fetch them
	url := fmt.Sprintf("%s%d", MangaPrefix, manga)
	log.Print("Downloading: ", url)
}

type Fetcher struct {
	ID      uint32
	Request <-chan FetchRequest
	Result  chan<- FetchResult
	Report  chan<- Status
}

func (f Fetcher) fetch() {
	for req := range f.Request {
		func(req FetchRequest) {
			file, err := os.Create(req.filedir)
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
			f.Result <- FetchResult{
				Ok:          true,
				Description: strconv.FormatInt(int64(n), 10) + " bytes were written",
			}
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
	filedir string
}

type FetchResult struct {
	Ok          bool
	Description string
}
