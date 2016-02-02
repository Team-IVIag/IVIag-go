package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
)

const (
	MangaPrefix   = "http://marumaru.in/b/manga/"
	ArchivePrefix = "http://www.mangaumaru.com/archives/"
)

var workerID, fetchID uint32

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

func (w Worker) Download(manga uint64) {
	url := fmt.Sprintf("%s%d", MangaPrefix, manga)
	log.Print("Downloading:", url)
}

type Fetcher struct {
	ID      uint32
	Request <-chan FetchRequest
	Result  chan<- FetchResult
	Report  chan<- Status
}

func (f Fetcher) fetch() {
	for req := range f.Request {
		file, err := os.Create(req.filedir)
		if err != nil {
			f.Result <- FetchResult{
				Ok:          false,
				Description: "Could not create file: " + err.Error(),
			}
			continue
		}
		func() {
			defer file.Close()
			resp, err := http.Get(req.URL)
			if err != nil {
				f.Result <- FetchResult{
					Ok:          false,
					Description: "HTTP request error: " + err.Error(),
				}
				return
			}
			defer resp.Body.Close()
			n, err := io.Copy(file, resp.Body)
			if err != nil {
				f.Result <- FetchResult{
					Ok:          false,
					Description: "File write error: " + err.Error(),
				}
			}
			f.Result <- FetchResult{
				Ok:          true,
				Description: strconv.FormatInt(n, 10) + " bytes were written",
			}
		}()
	}
}

const (
	FetchStart StatusID = iota
	FetchComplete
	FetchError
	PicFetchStart
	PicFetchComplete
	PicFetchError
	ListParseStart
	ListParseError
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
