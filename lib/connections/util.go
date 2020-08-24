// Copyright (C) 2016 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at http://mozilla.org/MPL/2.0/.

package connections

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/syncthing/syncthing/lib/osutil"
)

func fixupPort(uri *url.URL, defaultPort int) *url.URL {
	copyURI := *uri

	host, port, err := net.SplitHostPort(uri.Host)
	if err != nil && strings.Contains(err.Error(), "missing port") {
		// addr is on the form "1.2.3.4" or "[fe80::1]"
		host = uri.Host
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			// net.JoinHostPort will add the brackets again
			host = host[1 : len(host)-1]
		}
		copyURI.Host = net.JoinHostPort(host, strconv.Itoa(defaultPort))
	} else if err == nil && port == "" {
		// addr is on the form "1.2.3.4:" or "[fe80::1]:"
		copyURI.Host = net.JoinHostPort(host, strconv.Itoa(defaultPort))
	}

	return &copyURI
}

func getURLsForAllAdaptersIfUnspecified(network string, uri *url.URL) []*url.URL {
	ip, port, err := resolve(network, uri.Host)
	// Failed to resolve
	if err != nil || port == 0 {
		return nil
	}

	// Not an unspecified address, so no point of substituting with local
	// interface addresses as it's listening on a specific adapter anyway.
	if len(ip) != 0 && !ip.IsUnspecified() {
		return nil
	}

	hostPorts := getHostPortsForAllAdapters(port)
	addrs := make([]*url.URL, 0, len(hostPorts))
	for _, hostPort := range hostPorts {
		newUri := *uri
		newUri.Host = hostPort
		addrs = append(addrs, &newUri)
	}

	return addrs
}

func getHostPortsForAllAdapters(port int) []string {
	nets, err := osutil.GetLans()
	if err != nil {
		// Ignore failure.
		return nil
	}

	hostPorts := make([]string, 0, len(nets))

	portStr := strconv.Itoa(port)

	for _, network := range nets {
		// See: https://en.wikipedia.org/wiki/IPv6_address#Modified_EUI-64
		//      https://tools.ietf.org/html/rfc2464#section-4
		if len(network.IP) == net.IPv6len && network.IP[11] == 0xFF && network.IP[12] == 0xFE {
			// Contains a mac address.
			continue
		}
		if network.IP.IsLinkLocalUnicast() || (isV4Local(network.IP) && network.IP.IsGlobalUnicast()) {
			hostPorts = append(hostPorts, net.JoinHostPort(network.IP.String(), portStr))
		}
	}
	return hostPorts
}

func resolve(network, hostPort string) (net.IP, int, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
		if addr, err := net.ResolveTCPAddr(network, hostPort); err != nil {
			return net.IPv4zero, 0, err
		} else {
			return addr.IP, addr.Port, nil
		}
	case "udp", "udp4", "udp6":
		if addr, err := net.ResolveUDPAddr(network, hostPort); err != nil {
			return net.IPv4zero, 0, err
		} else {
			return addr.IP, addr.Port, nil
		}
	case "ip", "ip4", "ip6":
		if addr, err := net.ResolveIPAddr(network, hostPort); err != nil {
			return net.IPv4zero, 0, err
		} else {
			return addr.IP, 0, nil
		}
	}
	return net.IPv4zero, 0, net.UnknownNetworkError(network)
}

func isV4Local(ip net.IP) bool {
	// See https://go-review.googlesource.com/c/go/+/162998/
	// We only take the V4 part of that.
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 10 ||
			(ip4[0] == 172 && ip4[1]&0xf0 == 16) ||
			(ip4[0] == 192 && ip4[1] == 168)
	}
	return false
}
