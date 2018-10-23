// Copyright 2014 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package requester provides commands to run load tests and display results.
package requester

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// Max size of the buffer of result channel.
const maxResult = 1000000
const maxIdleConn = 500

type result struct {
	err           error
	statusCode    int
	duration      time.Duration
	connDuration  time.Duration // connection setup(DNS lookup + Dial up) duration
	dnsDuration   time.Duration // dns lookup duration
	reqDuration   time.Duration // request "write" duration
	resDuration   time.Duration // response "read" duration
	delayDuration time.Duration // delay between response and request
	contentLength int64
}

type ReqConfig struct {
	http.Header
	Method, Url   string
	Timeout       time.Duration
	RequestBody   [][]byte
	PauseDuration time.Duration
}

type Work struct {
	// Request is the request to be made.
	Request     *http.Request
	RequestBody []byte

	ReqConf *ReqConfig

	// N is the total number of requests to make.
	N int

	// C is the concurrency level, the number of concurrent workers to run.
	C int

	// H2 is an option to make HTTP/2 requests
	H2 bool

	// Timeout in seconds.
	Timeout int

	// RunTimeout in duration.
	RunTimeout time.Duration

	// Qps is the rate limit in queries per second.
	QPS float64

	// DisableCompression is an option to disable compression in response
	DisableCompression bool

	// DisableKeepAlives is an option to prevents re-use of TCP connections between different HTTP requests
	DisableKeepAlives bool

	// DisableRedirects is an option to prevent the following of HTTP redirects
	DisableRedirects bool

	// Output represents the output type. If "csv" is provided, the
	// output will be dumped as a csv stream.
	Output string

	// ProxyAddr is the address of HTTP proxy server in the format on "host:port".
	// Optional.
	ProxyAddr *url.URL

	// Writer is where results will be written. If nil, results are written to stdout.
	Writer io.Writer

	results chan *result
	stopCh  chan struct{}
	start   time.Time

	report *report
}

func (b *Work) writer() io.Writer {
	if b.Writer == nil {
		return os.Stdout
	}
	return b.Writer
}

// Run makes all the requests, prints the summary. It blocks until
// all work is done.
func (b *Work) Run() {
	//b.results = make(chan *result, min(b.C*1000, maxResult))
	b.stopCh = make(chan struct{}, b.C)
	//b.start = time.Now()
	//b.report = newReport(b.writer(), b.results, b.Output, b.N)
	//// Run the reporter first, it polls the result channel until it is closed.
	//go func() {
	//runReporter(b.report)
	//}()

	ctx, cancel := context.WithTimeout(context.Background(), b.RunTimeout)
	b.runWorkers(ctx)
	cancel()
	b.Finish()
}

func (b *Work) Stop() {
	// Send stop signal so that workers can stop gracefully.
	for i := 0; i < b.C; i++ {
		b.stopCh <- struct{}{}
	}
}

func (b *Work) Finish() {
	//close(b.results)
	//total := time.Now().Sub(b.start)
	// Wait until the reporter is done.
	//<-b.report.done
	//b.report.finalize(total)
}

func (b *Work) makeRequest(ctx context.Context, c *http.Client) {
	fmt.Println("[debug] makeRequest")
	//s := time.Now()
	//var size int64
	//var code int
	var dnsStart, connStart, resStart, reqStart, delayStart time.Time
	var dnsDuration, connDuration, reqDuration, delayDuration time.Duration
	//var resDuration

	pReader, pWriter := io.Pipe()
	req, err := http.NewRequest(b.Request.Method, b.Request.URL.String(), pReader)
	if err != nil {
		panic(err)
	}
	// deep copy of the Header
	req.Header = make(http.Header, len(b.Request.Header))
	for k, s := range b.Request.Header {
		req.Header[k] = append([]string(nil), s...)
	}
	//body := ioutil.NopCloser(bytes.NewReader(b.RequestBody))
	body := []byte("simitt pipe test")
	//req := cloneRequest(b.Request, b.RequestBody)

	ctx, cancel := context.WithTimeout(ctx, b.ReqConf.Timeout)

	go func(w io.WriteCloser) {
		defer w.Close()
		var pW = w

		for {
			select {
			case <-ctx.Done():
				fmt.Println("[debug] context done")
				return
			default:
				//fmt.Println("[debug] write to pipe")
				if _, err := pW.Write(body); err != nil {
					fmt.Println("[debug] error writing to pipe")
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}(pWriter)

	trace := &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			dnsStart = time.Now()
		},
		DNSDone: func(dnsInfo httptrace.DNSDoneInfo) {
			dnsDuration = time.Now().Sub(dnsStart)
		},
		GetConn: func(h string) {
			connStart = time.Now()
		},
		GotConn: func(connInfo httptrace.GotConnInfo) {
			if !connInfo.Reused {
				connDuration = time.Now().Sub(connStart)
			}
			reqStart = time.Now()
		},
		WroteRequest: func(w httptrace.WroteRequestInfo) {
			reqDuration = time.Now().Sub(reqStart)
			delayStart = time.Now()
		},
		GotFirstResponseByte: func() {
			delayDuration = time.Now().Sub(delayStart)
			resStart = time.Now()
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := c.Do(req)
	if err == nil {
		//size = resp.ContentLength
		//code = resp.StatusCode
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}
	cancel()
	//fmt.Println(size)
	//fmt.Println(code)
	//t := time.Now()
	//resDuration = t.Sub(resStart)
	//finish := t.Sub(s)
	//b.results <- &result{
	//statusCode:    code,
	//duration:      finish,
	//err:           err,
	//contentLength: size,
	//connDuration:  connDuration,
	//dnsDuration:   dnsDuration,
	//reqDuration:   reqDuration,
	//resDuration:   resDuration,
	//delayDuration: delayDuration,
	//}
}

func (b *Work) runWorker(ctx context.Context, client *http.Client, n int) {
	//var throttle <-chan time.Time
	//if b.QPS > 0 {
	//throttle = time.Tick(time.Duration(1e6/(b.QPS)) * time.Microsecond)
	//}

	if b.DisableRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	//connCtx, cancel := context.WithTimeout(ctx, b.ReqConf.Timeout)
	//connCtx, cancel := context.WithTimeout(ctx, time.Duration(5)*time.Second)
	for i := 0; i < n; i++ {
		// Check if application is stopped. Do not send into a closed channel.
		select {
		case <-b.stopCh:
			return
		default:
			//if b.QPS > 0 {
			//<-throttle
			//}
			b.makeRequest(ctx, client)
		}
	}
	//cancel()
}

func (b *Work) runWorkers(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(b.C)

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		MaxIdleConnsPerHost: min(b.C, maxIdleConn),
		DisableCompression:  b.DisableCompression,
		DisableKeepAlives:   b.DisableKeepAlives,
		Proxy:               http.ProxyURL(b.ProxyAddr),
	}
	if b.H2 {
		http2.ConfigureTransport(tr)
	} else {
		tr.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	}
	client := &http.Client{Transport: tr, Timeout: time.Duration(b.Timeout) * time.Second}

	// Ignore the case where b.N % b.C != 0.
	for i := 0; i < b.C; i++ {
		go func() {
			b.runWorker(ctx, client, b.N/b.C)
			wg.Done()
		}()
	}
	wg.Wait()
}

// cloneRequest returns a clone of the provided *http.Request.
// The clone is a shallow copy of the struct and its Header map.
func cloneRequest(r *http.Request, body []byte) *http.Request {
	// shallow copy of the struct
	r2 := new(http.Request)
	*r2 = *r
	// deep copy of the Header
	r2.Header = make(http.Header, len(r.Header))
	for k, s := range r.Header {
		r2.Header[k] = append([]string(nil), s...)
	}
	if len(body) > 0 {
		r2.Body = ioutil.NopCloser(bytes.NewReader(body))
	}
	return r2
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (w *Work) ErrorDist() map[string]int {
	return w.report.errorDist
}

func (w *Work) StatusCodes() map[int]int {
	return w.report.statusCodeDist
}