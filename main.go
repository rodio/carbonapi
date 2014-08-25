package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"code.google.com/p/gogoprotobuf/proto"

	cspb "github.com/grobian/carbonserver/carbonserverpb"
)

type zipper string

var Zipper zipper

var timeFormats = []string{"15:04 20060102", "20060102", "01/02/06"}

// dateParamToEpoch turns a passed string parameter into an epoch we can send to the Zipper
func dateParamToEpoch(s string, d int64) string {

	if s == "" {
		// return the default if nothing was passed
		return strconv.Itoa(int(d))
	}

	_, err := strconv.Atoi(s)
	if err == nil && len(s) > 8 {
		return s // We got a timestamp so returning it
	}

	if strings.Contains(s, "_") {
		s = strings.Replace(s, "_", " ", 1) // Go can't parse _ in date strings
	}

	for _, format := range timeFormats {
		t, err := time.Parse(format, s)
		if err == nil {
			return strconv.Itoa(int(t.Unix()))
		}
	}
	return strconv.Itoa(int(d))
}

// FIXME(dgryski): extract the http.Get + unproto code into its own function

func (z zipper) Find(metric string) (cspb.GlobResponse, error) {

	u, _ := url.Parse(string(z) + "/metrics/find/")

	u.RawQuery = url.Values{
		"query":  []string{metric},
		"format": []string{"protobuf"},
	}.Encode()

	resp, err := http.Get(u.String())
	if err != nil {
		log.Printf("Find: http.Get: %+v\n", err)
		return cspb.GlobResponse{}, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Find: ioutil.ReadAll: %+v\n", err)
		return cspb.GlobResponse{}, err
	}

	var pbresp cspb.GlobResponse

	err = proto.Unmarshal(body, &pbresp)
	if err != nil {
		log.Printf("Find: proto.Unmarshal: %+v\n", err)
		return cspb.GlobResponse{}, err
	}

	return pbresp, nil
}

func (z zipper) Render(metric, from, until string) (cspb.FetchResponse, error) {

	u, _ := url.Parse(string(z) + "/render/")

	u.RawQuery = url.Values{
		"target": []string{metric},
		"format": []string{"protobuf"},
		"from":   []string{from},
		"until":  []string{until},
	}.Encode()

	resp, err := http.Get(u.String())
	if err != nil {
		log.Printf("Render: http.Get: %s: %+v\n", metric, err)
		return cspb.FetchResponse{}, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Render: ioutil.ReadAll: %s: %+v\n", metric, err)
		return cspb.FetchResponse{}, err
	}

	var pbresp cspb.FetchResponse

	err = proto.Unmarshal(body, &pbresp)
	if err != nil {
		log.Printf("Render: proto.Unmarshal: %s: %+v\n", metric, err)
		return cspb.FetchResponse{}, err
	}

	return pbresp, nil
}

type limiter chan struct{}

func (l limiter) enter() { l <- struct{}{} }
func (l limiter) leave() { <-l }

var Limiter limiter

func renderHandler(w http.ResponseWriter, r *http.Request) {

	r.ParseForm()
	targets := r.Form["target"]
	from := r.FormValue("from")
	until := r.FormValue("until")

	// normalize from and until values
	from = dateParamToEpoch(from, time.Now().Add(-24*time.Hour).Unix())
	until = dateParamToEpoch(until, time.Now().Unix())

	var results []*cspb.FetchResponse
	// query zipper for find
	for _, target := range targets {
		glob, err := Zipper.Find(target)
		if err != nil {
			continue
		}

		// for each server in find response query render
		rch := make(chan *cspb.FetchResponse, len(glob.GetMatches()))
		for _, m := range glob.GetMatches() {
			go func(m *cspb.GlobMatch) {
				Limiter.enter()
				if m.GetIsLeaf() {
					r, err := Zipper.Render(m.GetPath(), from, until)
					if err != nil {
						rch <- nil
					} else {
						rch <- &r
					}
				} else {
					rch <- nil
				}
				Limiter.leave()
			}(m)
		}

		for i := 0; i < len(glob.GetMatches()); i++ {
			r := <-rch
			if r != nil {
				results = append(results, r)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	jEnc := json.NewEncoder(w)
	jEnc.Encode(results)
}

func main() {

	z := flag.String("z", "", "zipper")
	port := flag.Int("p", 8080, "port")
	l := flag.Int("l", 20, "concurrency limit")

	flag.Parse()

	if *z == "" {
		log.Fatal("no zipper (-z) provided")
	}

	Limiter = make(chan struct{}, *l)

	if _, err := url.Parse(*z); err != nil {
		log.Fatal("unable to parze zipper:", err)
	}

	Zipper = zipper(*z)

	http.HandleFunc("/render/", renderHandler)

	log.Println("listening on port", *port)
	log.Fatalln(http.ListenAndServe(":"+strconv.Itoa(*port), nil))

}