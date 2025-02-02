package backend

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/stellar/kelp/plugins"
)

type fetchPriceInput struct {
	Type    string `json:"type"`
	FeedURL string `json:"feed_url"`
}

type fetchPriceOutput struct {
	Price float64 `json:"price"`
}

func (s *APIServer) fetchPrice(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	bodyBytes, e := ioutil.ReadAll(r.Body)
	if e != nil {
		s.writeErrorJson(w, fmt.Sprintf("error reading request input: %s", e))
		return
	}
	log.Printf("requestJson: %s\n", string(bodyBytes))

	var input fetchPriceInput
	e = json.Unmarshal(bodyBytes, &input)
	if e != nil {
		s.writeErrorJson(w, fmt.Sprintf("error unmarshaling json: %s; bodyString = %s", e, string(bodyBytes)))
		return
	}

	pf, e := plugins.MakePriceFeed(input.Type, input.FeedURL)
	if e != nil {
		s.writeErrorJson(w, fmt.Sprintf("unable to make price feed: %s", e))
		return
	}
	price, e := pf.GetPrice()
	if e != nil {
		s.writeErrorJson(w, fmt.Sprintf("unable to fetch price: %s", e))
		return
	}

	// force sleep for at least 1 second to cause some artificial delay
	minRequestTime := 1 * time.Second
	elapsed := time.Now().Sub(startTime)
	nanos := minRequestTime.Nanoseconds() - elapsed.Nanoseconds()
	log.Printf("force sleep for %d nanoseconds\n", nanos)
	time.Sleep(time.Duration(nanos))

	s.writeJson(w, fetchPriceOutput{
		Price: price,
	})
}
