package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
)

func main() {
	maxPics := flag.Int64("max", -1, "다운로드할 최대 이미지 수")
	workers := flag.Uint64("worker", 5, "한 번에 동시에 다운로드할 만화 수")
	fetchers := flag.Uint64("fetch", 1, "만화당 동시에 다운로드할 이미지 수")
	flag.Parse()
	mangas := flag.Args()

	log.Println(mangas, *maxPics, *workers, fetchers)

	targets := make([]uint64, len(mangas))
	for i, manga := range mangas {
		m, err := strconv.ParseUint(manga, 10, 64)
		if err != nil {
			log.Fatal(err)
		}
		targets[i] = m
	}
	if *fetchers == 0 || *workers == 0 || *maxPics == 0 {
		fmt.Println("잘못된 인자입니다.")
		os.Exit(1)
	}

	dl := new(Downloader)
	dl.Init(*workers, *fetchers, *maxPics)
	dl.Start(targets)

	<-make(chan bool)
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
}

func (d *Downloader) Start(mangas []uint64) {
	for _, manga := range mangas {
		d.target <- manga
	}
}
