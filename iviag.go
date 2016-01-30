package main

import (
	"flag"
	"log"
)

func main() {
	maxPics := flag.Int64("max", -1, "다운로드할 최대 이미지 수")
	workers := flag.Uint64("worker", 5, "한 번에 동시에 다운로드할 이미지 수")
	flag.Parse()
	mangas := flag.Args()
	log.Println(mangas, *maxPics, *workers)
}
