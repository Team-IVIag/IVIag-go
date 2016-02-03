package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
		Value: "7c3e7edb2923fae1d03dc56ba296f6da",
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

	wg := new(sync.WaitGroup)
	wg.Add(int(*downloaders))
	for i := uint64(0); i < *downloaders; i++ {
		dl := new(Downloader)
		dl.Init(*workers, *fetchers, *maxPics)
		go dl.Start(targets, wg)
	}
	close(targets)
	wg.Wait()
	log.Print("All tasks were done.")
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
	Seq     uint64
	Title   string
	Subject string
	Wait    *sync.WaitGroup
}

func GetArchives(manga uint64) (title string, mangas []Archive, err error) {
	var doc *goquery.Document
	doc, err = goquery.NewDocument(MangaPrefix + strconv.FormatUint(manga, 10))
	if err != nil {
		return
	}
	links := make([]Archive, 0)
	var seq uint64
	title = Filter(doc.Find("div .subject").Find("h1").Text(), "_")
	if title == "" {
		title = "Untitled"
	}
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
					Seq:     seq,
					Title:   title,
					Subject: sub,
				})
				seq++
			}
		}
	})
	return title, links, nil
}

func GetPics(archive uint64) (rawhtml string, urls []string, err error) {
	req, err := http.NewRequest("GET", ArchivePrefix+strconv.FormatUint(archive, 10), nil)
	if err != nil {
		return "", nil, fmt.Errorf("Request creation error: %s", err.Error())
	}
	req.Header.Set("User-Agent", UserAgent)
	req.AddCookie(Cookie)
	resp, err := new(http.Client).Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("HTTP request error: %s", err.Error())
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	io.Copy(buf, resp.Body)
	rawhtml = buf.String()
	var doc *goquery.Document
	doc, err = goquery.NewDocumentFromReader(buf)
	if err != nil {
		return
	}
	urls = make([]string, 0)
	doc.Find("div .entry-content").Find("img").Each(func(i int, s *goquery.Selection) {
		if img, ok := s.Attr("data-lazy-src"); ok {
			urls = append(urls, img)
		}
	})
	return
}

type Downloader struct {
	Workers  []*Worker
	Wait     *sync.WaitGroup
	workers  uint64
	fetchers uint64
	maxPics  int64
	target   chan<- Archive
	report   <-chan Status
}

func (d *Downloader) Init(workers, fetchers uint64, maxPics int64) {
	d.workers, d.fetchers, d.maxPics = workers, fetchers, maxPics
	d.Workers = make([]*Worker, workers)
	d.Wait = new(sync.WaitGroup)
	tc, rc := make(chan Archive, 1), make(chan Status, 30)
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

func (d *Downloader) Start(mangas <-chan uint64, wg *sync.WaitGroup) {
	for manga := range mangas {
		log.Printf("Parsing archive list: %s%d", MangaPrefix, manga)
		title, links, err := GetArchives(manga)
		if err != nil {
			log.Printf("Archive list parse error: %s", err.Error())
			continue
		}
		os.MkdirAll(title, os.ModeDir)
		d.Wait.Add(len(links))
		for _, link := range links {
			link.Wait = d.Wait
			d.target <- link
		}
	}
	d.Wait.Wait()
	wg.Done()
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
		dir := target.Title + "/" + strconv.FormatUint(target.Seq, 10) + "_" + Filter(target.Subject, "_")
		os.MkdirAll(dir, os.ModeDir)
		log.Printf("Parsing archive: %s%d", ArchivePrefix, target.ID)
		raw, imgs, err := GetPics(target.ID)
		if err != nil {
			log.Printf("Archive parse error: %s", err.Error())
		}
		log.Printf("Archive parse done for: %s%d", ArchivePrefix, target.ID)
		func() {
			file, err := os.Create(dir + "/raw.html")
			if err == nil {
				defer file.Close()
			}
			file.WriteString(raw)
		}()
		wg := new(sync.WaitGroup)
		wg.Add(len(imgs))
		for i, img := range imgs {
			w.FRequest <- FetchRequest{
				URL:     img,
				FileDir: target.Title + "/" + strconv.FormatUint(target.Seq, 10) + "_" + Filter(target.Subject+"/"+strconv.Itoa(i)+".jpg", "_"),
				Wait:    wg,
			}
		}
		log.Printf("Archive fetch request for %d(%s) sent. Waiting now.", target.ID, target.Subject)
		wg.Wait()
		files, err := filepath.Glob(dir + "/*.jpg")
		if err != nil {
			files = make([]string, 0)
		}
		target.Wait.Done()
		log.Printf("Archive fetch complete for %d(%s). Pics count: %d", target.ID, target.Subject, len(files))
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
		// log.Printf("Fetch start: %s -> %s", req.URL, req.FileDir)
		func(req FetchRequest) {
			defer req.Wait.Done()
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
			_, err = file.WriteString(s)
			/*
				            f.Result <- FetchResult{
								Ok:          true,
								Description: strconv.FormatInt(int64(n), 10) + " bytes were written",
							}
			*/
			// log.Printf("Fetch to %s succeeded: %d bytes were written", req.FileDir, n)
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
	Wait    *sync.WaitGroup
}

type FetchResult struct {
	Ok          bool
	Description string
}
