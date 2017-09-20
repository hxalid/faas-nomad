package handlers

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/faas-nomad/consul"
	"github.com/nicholasjackson/ultraclient"
	cache "github.com/patrickmn/go-cache"
)

// MakeProxy creates a proxy for HTTP web requests which can be routed to a function.
func MakeProxy(client ProxyClient, resolver consul.ServiceResolver) http.HandlerFunc {

	c := cache.New(5*time.Minute, 10*time.Minute)
	p := &Proxy{
		lbCache:  c,
		client:   client,
		resolver: resolver,
	}

	return func(rw http.ResponseWriter, r *http.Request) {
		p.ServeHTTP(rw, r)
	}
}

// Proxy is a http.Handler which implements the ability to call a downstream function
type Proxy struct {
	lbCache  *cache.Cache
	client   ProxyClient
	resolver consul.ServiceResolver
}

func (p *Proxy) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	if r.Method != "POST" {
		rw.WriteHeader(http.StatusBadRequest)
		return
	}

	service := r.Context().Value(FunctionNameCTXKey).(string)

	urls, _ := p.resolver.Resolve(service)

	// hack for docker for mac, need real implementation
	lb := p.getLoadbalancer(service, urls)
	lb.Do(func(endpoint url.URL) error {

		address := endpoint.String()
		if strings.Contains(address, "127.0.0.1") {
			address = strings.Replace(address, "127.0.0.1", "docker.for.mac.localhost", 1)
		}

		fmt.Println(address)

		return p.client.CallAndReturnResponse(address, rw, r)
	})
}

func (p *Proxy) getLoadbalancer(service string, endpoints []string) ultraclient.Client {
	urls := make([]url.URL, 0)
	for _, e := range endpoints {
		url, err := url.Parse(e)
		if err != nil {
			log.Println(err)
		} else {
			urls = append(urls, *url)
		}
	}

	if lb, ok := p.lbCache.Get(service); ok {
		l := lb.(ultraclient.Client)
		l.UpdateEndpoints(urls)
		return l
	}

	lb := createLoadbalancer(urls)
	p.lbCache.Set(service, lb, cache.DefaultExpiration)

	return lb
}

func createLoadbalancer(endpoints []url.URL) ultraclient.Client {
	lb := ultraclient.RoundRobinStrategy{}
	bs := ultraclient.ExponentialBackoff{}

	config := ultraclient.Config{
		Timeout:                5 * time.Second,
		MaxConcurrentRequests:  500,
		ErrorPercentThreshold:  25,
		DefaultVolumeThreshold: 10,
		Retries:                3,
		Endpoints:              endpoints,
	}

	return ultraclient.NewClient(config, &lb, &bs)
}
