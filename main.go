package main

import (
	"fmt"
	"sync"
)

func readFiles(filename string, wg *sync.WaitGroup, ch chan int) {
	defer wg.Done()
	fmt.Printf("%s\n", filename)
	for i := 0; i < 20; i++ {
		ch <- i
	}
}
func encodeFiles(id int, wg *sync.WaitGroup, ch chan int, outch chan int) {
	defer wg.Done()
	for item := range ch {
		fmt.Printf("thread %d run %d\n", id, item)
		outch <- item
	}
}
func writeFiles(filename string, wg *sync.WaitGroup, ch chan int) {
	defer wg.Done()
	fmt.Printf("%s\n", filename)
	for item := range ch {
		fmt.Printf("thread %s run %d\n", filename, item)
	}
}

func main() {
	wgRead := &sync.WaitGroup{}
	wgEncode := &sync.WaitGroup{}
	wgWrite := &sync.WaitGroup{}
	readEncodeCh := make(chan int, 8)
	encodeWriteCh := make(chan int, 8)
	wgRead.Add(1)
	go readFiles("hello", wgRead, readEncodeCh)
	for i := 0; i < 8; i++ {
		wgEncode.Add(1)
		go encodeFiles(i, wgEncode, readEncodeCh, encodeWriteCh)
	}
	wgWrite.Add(1)
	go writeFiles("hi", wgWrite, encodeWriteCh)
	wgRead.Wait()
	close(readEncodeCh)
	wgEncode.Wait()
	close(encodeWriteCh)
	wgWrite.Wait()
}
