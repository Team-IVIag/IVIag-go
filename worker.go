package main

import (
	"fmt"
	"log"
)

const (
	MangaPrefix   = "http://marumaru.in/b/manga/"
	ArchivePrefix = "http://www.mangaumaru.com/archives/"
)

type Worker struct {
	ID       uint
	Fetchers []*Fetcher
	Targets  <-chan uint64
	Report   chan<- Status
	FRequest chan<- FetchRequest
	FResult  <-chan FetchResult
}

func NewWorker(ID uint, fetchers uint64, tc <-chan uint64, rc chan<- Status) *Worker {
	w := new(Worker)
	w.ID = ID
	w.Targets, w.Report = tc, rc
	w.Fetchers = make([]*Fetcher, fetchers)
	w.FRequest = make(chan FetchRequest, 30)
	w.FResult = make(chan FetchResult, 30*fetchers)
	return w
}

func (w *Worker) Download(manga uint64) {
	url := fmt.Sprintf("%s%d", MangaPrefix, manga)
	log.Print("Downloading:", url)
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

type Fetcher struct {
	ID      uint
	Request <-chan FetchRequest
	Result  chan<- FetchResult
}

type FetchRequest struct {
	URL     string
	filedir string
}

type FetchResult struct {
	Ok          bool
	Description string
}
