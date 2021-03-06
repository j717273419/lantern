// Utility for checking if fallback servers are working properly.
// It outputs failing servers info in STDOUT.  This allows this program to be
// used for automated testing of the fallback servers as a cron job.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/getlantern/flashlight/client"
	"github.com/getlantern/golog"
	"io/ioutil"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
)

var (
	help          = flag.Bool("help", false, "Get usage help")
	fallbacksFile = flag.String("fallbacks", "fallbacks.json", "File containing json array of fallback information")
	numConns      = flag.Int("connections", 1, "Number of simultaneous connections")

	expectedBody = "Google is built by a large team of engineers, designers, researchers, robots, and others in many different sites across the globe. It is updated continuously, and built with more tools and technologies than we can shake a stick at. If you'd like to help us out, see google.com/careers.\n"
)

var (
	log = golog.LoggerFor("checkfallbacks")
)

func main() {
	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(1)
	}

	numcores := runtime.NumCPU()
	runtime.GOMAXPROCS(numcores)
	log.Debugf("Using all %d cores on machine", numcores)

	fallbacks := loadFallbacks(*fallbacksFile)
	for err := range *testAllFallbacks(fallbacks) {
		if err != nil {
			fmt.Printf("[failed fallback check] %v\n", err)
		}
	}
}

// Load the fallback servers list file. Failure to do so will result in
// exiting the program.
func loadFallbacks(filename string) (fallbacks []client.ChainedServerInfo) {
	if filename == "" {
		log.Error("Please specify a fallbacks file")
		flag.Usage()
		os.Exit(2)
	}

	fileBytes, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatalf("Unable to read fallbacks file at %s: %s", filename, err)
	}

	err = json.Unmarshal(fileBytes, &fallbacks)
	if err != nil {
		log.Fatalf("Unable to unmarshal json from %v: %v", filename, err)
	}

	// Replace newlines in cert with newline literals
	for _, fb := range fallbacks {
		fb.Cert = strings.Replace(fb.Cert, "\n", "\\n", -1)
	}
	return
}

// Test all fallback servers
func testAllFallbacks(fallbacks []client.ChainedServerInfo) (errors *chan error) {
	errorsChan := make(chan error)
	errors = &errorsChan

	// Make
	fbChan := make(chan client.ChainedServerInfo)
	// Channel fallback servers on-demand
	go func() {
		for _, val := range fallbacks {
			fbChan <- val
		}
		close(fbChan)
	}()

	// Spawn goroutines and wait for them to finish
	go func() {
		workersWg := sync.WaitGroup{}

		log.Debugf("Spawning %d workers\n", *numConns)

		workersWg.Add(*numConns)
		for i := 0; i < *numConns; i++ {
			// Worker: consume fallback servers from channel and signal
			// Done() when closed (i.e. range exits)
			go func(i int) {
				for fb := range fbChan {
					*errors <- testFallbackServer(&fb, i)
				}
				workersWg.Done()
			}(i + 1)
		}
		workersWg.Wait()

		close(errorsChan)
	}()

	return
}

// Perform the test of an individual server
func testFallbackServer(fb *client.ChainedServerInfo, workerId int) (err error) {
	// Test connectivity
	fb.Pipelined = true
	dialer, err := fb.Dialer()
	if err != nil {
		return fmt.Errorf("%v: error building dialer: %v", fb.Addr, err)
	}
	c := &http.Client{
		Transport: &http.Transport{
			Dial: dialer.Dial,
		},
	}
	req, err := http.NewRequest("GET", "http://www.google.com/humans.txt", nil)
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("%v: requesting humans.txt failed: %v", fb.Addr, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Debugf("Unable to close response body: %v", err)
		}
	}()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%v: bad status code: %v", fb.Addr, resp.StatusCode)
	}
	bytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%v: error reading response body: %v", fb.Addr, err)
	}
	body := string(bytes)
	if body != expectedBody {
		return fmt.Errorf("%v: wrong body: %s", fb.Addr, body)
	}

	log.Debugf("Worker %d: Fallback %v OK.\n", workerId, fb.Addr)
	return nil
}
