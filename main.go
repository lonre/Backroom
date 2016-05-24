package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"

	"github.com/lonre/backroom/strategy"
)

var (
	fAddr  = flag.String("addr", "127.0.0.1:5000", "Listen on address")
	fLog   = flag.String("log", "log", "Log dir path")
	fProxy = flag.String("proxy", "", "single upstream proxy, 127.0.0.1:8080")
	fRedis = flag.String("redis", "", "Proxy list redis db url, 127.0.0.1:6379")
	fDebug = flag.Bool("debug", false, "Enable debug mode")
)

var directHTTPTransport = &http.Transport{
	Proxy:             nil,
	DisableKeepAlives: true,
	Dial: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).Dial,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

var proxyHTTPTransport = &http.Transport{
	Proxy: func(*http.Request) (*url.URL, error) {
		return strat.ProxyURL()
	},
	DisableKeepAlives: true,
	Dial: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).Dial,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// UpstreamProxyStrategy interface
type UpstreamProxyStrategy interface {
	ProxyURL() (*url.URL, error)
	RetryLimit() int
	RetryFallback() bool
}

var listenAddress string
var debugModeEnabled bool
var strat UpstreamProxyStrategy

func initBackroom() {
	flag.Parse()
	debugModeEnabled = *fDebug
	var modeName = "debug"
	log.SetLevel(log.DebugLevel)
	if !debugModeEnabled {
		modeName = "release"
		log.SetLevel(log.InfoLevel)
	}

	strat = initStrag()

	logDirPath, err := filepath.Abs(*fLog)
	if err != nil {
		log.Fatal(err)
	}
	log.Infof("Log to dir: %s", logDirPath)
	if _, _err := os.Stat(logDirPath); os.IsNotExist(_err) {
		os.MkdirAll(logDirPath, 0644)
	}

	listenAddress = fmt.Sprintf("%s", *fAddr)
	log.Infof("About to listen on: %s using strategy: %v", listenAddress, strat)

	logFile, err := os.OpenFile(filepath.Join(logDirPath, "log_"+modeName+".log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal(err)
	}
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})
	log.SetOutput(logFile)
}

func initStrag() (stra UpstreamProxyStrategy) {
	switch {
	case *fProxy != "":
		return &strategy.Single{URL: *fProxy}
	case *fRedis != "":
		return strategy.NewRedis(*fRedis)
	}
	return &strategy.None{}
}

func main() {
	initBackroom()

	proxyServer := &http.Server{
		Addr:           listenAddress,
		Handler:        http.HandlerFunc(proxyHandler),
		ReadTimeout:    120 * time.Second,
		WriteTimeout:   120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	log.Fatal(proxyServer.ListenAndServe())
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	if r.Method != "CONNECT" && !r.URL.IsAbs() {
		// not proxy request, response with 501 status code
		log.Warnf("non proxy %s %s %s %s", remoteIP, r.Method, r.URL.String(), r.UserAgent())
		http.Error(w, fmt.Sprintf("%d %s", 501, http.StatusText(501)), 501)
		return
	}

	log.Infof("proxy %s %s %s %s", remoteIP, r.Method, r.URL.String(), r.UserAgent())

	switch r.Method {
	case "CONNECT":
		httpsProxy(w, r)
	default:
		httpProxy(w, r)
	}
}

func httpProxy(w http.ResponseWriter, r *http.Request) {
	var resp *http.Response
	var err error

	removeHopByHopHeaders(r)

	for try := 0; try <= strat.RetryLimit(); try++ {
		if try == strat.RetryLimit() && strat.RetryFallback() {
			resp, err = directHTTPTransport.RoundTrip(r)
		} else {
			resp, err = proxyHTTPTransport.RoundTrip(r)
		}
		if err != nil {
			log.Warnf("upstream request retry: %d error: %s", try, err.Error())
			if try == strat.RetryLimit() {
				w.WriteHeader(502)
				return
			}
			continue
		}
		break
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	written, err := io.Copy(w, resp.Body)
	if err != nil {
		log.Warnf("http proxy copy response err: %s", err.Error())
	} else {
		log.Debugf("copyed %d bytes from %s to %s", written, r.URL.Host, r.RemoteAddr)
	}
}

func httpsProxy(w http.ResponseWriter, r *http.Request) {
	hj, _ := w.(http.Hijacker)
	clientConn, _, err := hj.Hijack()
	if err != nil {
		log.Warnf("proxy connection hijack failed: %s", err.Error())
		return
	}

	var proxyConn net.Conn
	for try := 0; try <= strat.RetryLimit(); try++ {
		if try == strat.RetryLimit() && strat.RetryFallback() {
			proxyConn, err = proxyConnection(&strategy.None{}, r)
		} else {
			proxyConn, err = proxyConnection(strat, r)
		}
		if err != nil {
			log.Warnf("upstream proxy connection retry: %d error: %s", try, err.Error())
			if try == strat.RetryLimit() {
				fmt.Fprint(clientConn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
				log.Debugf("close response client connection %s", clientConn.RemoteAddr())
				clientConn.Close()
				return
			}
			continue
		}
		break
	}

	fmt.Fprint(clientConn, "HTTP/1.1 200 Connection established\r\n\r\n")

	go copyAndClose(proxyConn, clientConn)
	go copyAndClose(clientConn, proxyConn)
}

func proxyConnection(ups UpstreamProxyStrategy, r *http.Request) (net.Conn, error) {
	proxyURL, err := ups.ProxyURL()
	if err != nil {
		return nil, err
	}

	var dialHost string
	if proxyURL == nil {
		dialHost = r.URL.Host
	} else {
		dialHost = proxyURL.Host
	}

	proxyConn, err := net.DialTimeout("tcp", dialHost, 60*time.Second)
	log.Debugf("dial to upstream host: %v", dialHost)
	if err != nil {
		return nil, err
	}

	if proxyURL != nil {
		fmt.Fprintf(proxyConn, "CONNECT %s HTTP/1.1\r\n\r\n", r.URL.Host)
		status, err := textproto.NewReader(bufio.NewReader(proxyConn)).ReadLine()
		if err != nil {
			proxyConn.Close()
			return nil, err
		}
		log.Debugf("upstream proxy resp: %s", status)

		if !strings.Contains(status, "200") {
			proxyConn.Close()
			return nil, fmt.Errorf("upstream proxy: %s response: %s", proxyURL.Host, status)
		}
	}
	return proxyConn, nil
}

func copyAndClose(dst, src net.Conn) {
	defer src.Close()

	written, err := io.Copy(dst, src)
	if err != nil {
		log.Warnf("copy from %s to %s, error: %s", src.RemoteAddr(), dst.RemoteAddr(), err)
		return
	}
	log.Debugf("copyed %d bytes from %s to %s", written, src.RemoteAddr(), dst.RemoteAddr())
}

func removeHopByHopHeaders(r *http.Request) {
	r.RequestURI = ""
	// remove Hop-by-hop headers  https://www.ietf.org/rfc/rfc2616.txt
	r.Header.Del("Connection")
	r.Header.Del("Keep-Alive")
	r.Header.Del("Proxy-Authenticate")
	r.Header.Del("Proxy-Authorization")
	r.Header.Del("TE")
	r.Header.Del("Trailers")
	r.Header.Del("Transfer-Encoding")
	r.Header.Del("Upgrade")
}

func copyHeaders(dst, src http.Header) {
	for k := range dst {
		dst.Del(k)
	}
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
