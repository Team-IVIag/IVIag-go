package main

type Worker struct {
	ID      uint
	Request <-chan [2]string //URL, filename
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
