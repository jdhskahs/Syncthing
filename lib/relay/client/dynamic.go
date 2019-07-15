// Copyright (C) 2015 Audrius Butkevicius and Contributors (see the CONTRIBUTORS file).

package client

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/syncthing/syncthing/lib/osutil"
	"github.com/syncthing/syncthing/lib/rand"
	"github.com/syncthing/syncthing/lib/relay/protocol"
	"github.com/syncthing/syncthing/lib/sync"
)

type dynamicClient struct {
	commonClient

	pooladdr *url.URL
	certs    []tls.Certificate
	timeout  time.Duration

	client RelayClient
}

func newDynamicClient(uri *url.URL, certs []tls.Certificate, invitations chan protocol.SessionInvitation, timeout time.Duration) RelayClient {
	c := &dynamicClient{
		pooladdr: uri,
		certs:    certs,
		timeout:  timeout,
	}
	c.commonClient = newCommonClient(invitations, c.serve)
	return c
}

func (c *dynamicClient) serve(stop chan struct{}) error {
	uri := *c.pooladdr

	// Trim off the `dynamic+` prefix
	uri.Scheme = uri.Scheme[8:]

	l.Debugln(c, "looking up dynamic relays")

	data, err := http.Get(uri.String())
	if err != nil {
		l.Debugln(c, "failed to lookup dynamic relays", err)
		return err
	}

	var ann dynamicAnnouncement
	err = json.NewDecoder(data.Body).Decode(&ann)
	data.Body.Close()
	if err != nil {
		l.Debugln(c, "failed to lookup dynamic relays", err)
		return err
	}

	var addrs []string
	for _, relayAnn := range ann.Relays {
		ruri, err := url.Parse(relayAnn.URL)
		if err != nil {
			l.Debugln(c, "failed to parse dynamic relay address", relayAnn.URL, err)
			continue
		}
		l.Debugln(c, "found", ruri)
		addrs = append(addrs, ruri.String())
	}

	defer func() {
		c.mut.RLock()
		if c.client != nil {
			c.client.Stop()
		}
		c.mut.RUnlock()
	}()

	for _, addr := range relayAddressesOrder(addrs) {
		select {
		case <-stop:
			l.Debugln(c, "stopping")
			return nil
		default:
			ruri, err := url.Parse(addr)
			if err != nil {
				l.Debugln(c, "skipping relay", addr, err)
				continue
			}
			client := newStaticClient(ruri, c.certs, c.invitations, c.timeout)
			c.mut.Lock()
			c.client = client
			c.mut.Unlock()

			c.client.Serve()

			c.mut.Lock()
			c.client = nil
			c.mut.Unlock()
		}
	}
	l.Debugln(c, "could not find a connectable relay")
	return fmt.Errorf("could not find a connectable relay")
}

func (c *dynamicClient) Error() error {
	c.mut.RLock()
	defer c.mut.RUnlock()
	if c.client == nil {
		return c.commonClient.Error()
	}
	return c.client.Error()
}

func (c *dynamicClient) Latency() time.Duration {
	c.mut.RLock()
	defer c.mut.RUnlock()
	if c.client == nil {
		return time.Hour
	}
	return c.client.Latency()
}

func (c *dynamicClient) String() string {
	return fmt.Sprintf("DynamicClient:%p:%s@%s", c, c.URI(), c.pooladdr)
}

func (c *dynamicClient) URI() *url.URL {
	c.mut.RLock()
	defer c.mut.RUnlock()
	if c.client == nil {
		return nil
	}
	return c.client.URI()
}

// This is the announcement received from the relay server;
// {"relays": [{"url": "relay://10.20.30.40:5060"}, ...]}
type dynamicAnnouncement struct {
	Relays []struct {
		URL string
	}
}

// relayAddressesOrder checks the latency to each relay, rounds latency down to
// the closest 50ms, and puts them in buckets of 50ms latency ranges. Then
// shuffles each bucket, and returns all addresses starting with the ones from
// the lowest latency bucket, ending with the highest latency buceket.
func relayAddressesOrder(input []string) []string {
	buckets := make(map[int][]string)

	wg := sync.NewWaitGroup()
	wg.Add(len(input))
	results := make(chan urlWithLatency, len(input))
	for _, relay := range input {
		go func(irelay string) {
			latency, err := osutil.GetLatencyForURL(irelay)
			if err != nil {
				latency = time.Hour
			}
			results <- urlWithLatency{irelay, latency}
			wg.Done()
		}(relay)
	}

	wg.Wait()
	close(results)

	for result := range results {
		id := int(result.latency/time.Millisecond) / 50
		buckets[id] = append(buckets[id], result.url)
	}

	var ids []int
	for id, bucket := range buckets {
		rand.Shuffle(bucket)
		ids = append(ids, id)
	}

	sort.Ints(ids)

	addresses := make([]string, len(input))

	for _, id := range ids {
		addresses = append(addresses, buckets[id]...)
	}

	return addresses
}

type urlWithLatency struct {
	url     string
	latency time.Duration
}
