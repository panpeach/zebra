package helpers

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/MeABc/glog"
	"github.com/cloudflare/golibs/lrucache"
	"github.com/miekg/dns"
)

const (
	DefaultDNSCacheExpiry time.Duration = 600 * time.Second
)

type Resolver struct {
	LRUCache    lrucache.Cache
	BlackList   lrucache.Cache
	DNSServer   string
	DNSExpiry   time.Duration
	DisableIPv6 bool
	ForceIPv6   bool
	network     string // name of the network (for example, "tcp", "udp")
}

func (r *Resolver) LookupHost(name string) ([]string, error) {
	ips, err := r.LookupIP(name)
	if err != nil {
		return nil, err
	}

	addrs := make([]string, len(ips))
	for i, ip := range ips {
		addrs[i] = ip.String()
	}

	return addrs, nil
}

func (r *Resolver) LookupIP(name string) ([]net.IP, error) {
	if r.LRUCache != nil {
		if v, ok := r.LRUCache.GetNotStale(name); ok {
			switch v.(type) {
			case []net.IP:
				return v.([]net.IP), nil
			case string:
				return r.LookupIP(v.(string))
			default:
				return nil, fmt.Errorf("LookupIP: cannot convert %T(%+v) to []net.IP", v, v)
			}
		}
	}

	if ip := net.ParseIP(name); ip != nil {
		return []net.IP{ip}, nil
	}

	lookupIP := r.lookupIP1
	if r.DNSServer != "" {
		lookupIP = r.lookupIP2
	}

	ips, err := lookupIP(name)
	if err == nil {
		if r.BlackList != nil {
			ips1 := ips[:0]
			for _, ip := range ips {
				if _, ok := r.BlackList.GetQuiet(ip.String()); !ok {
					ips1 = append(ips1, ip)
				}
			}
			ips = ips1
		}

		if r.LRUCache != nil && len(ips) > 0 {
			if r.DNSExpiry == 0 {
				r.LRUCache.Set(name, ips, time.Now().Add(DefaultDNSCacheExpiry))
			} else {
				r.LRUCache.Set(name, ips, time.Now().Add(r.DNSExpiry))
			}
		}
	}

	glog.V(2).Infof("LookupIP(%#v) return %+v, err=%+v", name, ips, err)
	return ips, err
}

func (r *Resolver) lookupIP1(name string) ([]net.IP, error) {
	ips, err := LookupIP(name)
	if err != nil {
		return nil, err
	}

	ips1 := ips[:0]
	for _, ip := range ips {
		if strings.Contains(ip.String(), ":") {
			if r.ForceIPv6 || !r.DisableIPv6 {
				ips1 = append(ips1, ip)
			}
		} else {
			if !r.ForceIPv6 {
				ips1 = append(ips1, ip)
			}
		}
	}

	return ips1, nil
}

func (r *Resolver) lookupIP2(name string) ([]net.IP, error) {
	c := &dns.Client{
		Timeout: 5 * time.Second,
	}
	m := &dns.Msg{}

	switch {
	case r.ForceIPv6:
		m.SetQuestion(dns.Fqdn(name), dns.TypeAAAA)
	case r.DisableIPv6:
		m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	default:
		m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	}

	ip0, port0, _, err := ParseIPPort(r.DNSServer)
	if err != nil {
		return nil, err
	}
	if port0 == "" {
		port0 = "53"
	}

	reply, _, err := c.Exchange(m, net.JoinHostPort(ip0.String(), "53"))
	if err != nil {
		return nil, err
	}

	if len(reply.Answer) < 1 {
		return nil, fmt.Errorf("no Answer from dns server %v", r.DNSServer)
	}

	ips := make([]net.IP, 0, 4)
	var ip net.IP

	for _, rr := range reply.Answer {
		switch rr.(type) {
		case *dns.AAAA:
			ip = rr.(*dns.AAAA).AAAA
		case *dns.A:
			ip = rr.(*dns.A).A
		}
		if ip != nil {
			ips = append(ips, ip)
		}
	}

	return ips, nil
}

// https://rosettacode.org/wiki/Parse_an_IP_Address#Go
func ParseIPPort(s string) (ip net.IP, port, space string, err error) {
	ip = net.ParseIP(s)
	if ip == nil {
		var host string
		host, port, err = net.SplitHostPort(s)
		if err != nil {
			return
		}
		if port != "" {
			// This check only makes sense if service names are not allowed
			if _, err = strconv.ParseUint(port, 10, 16); err != nil {
				return
			}
		}
		ip = net.ParseIP(host)
	}
	if ip == nil {
		err = errors.New("invalid address format")
	} else {
		space = "IPv6"
		if ip4 := ip.To4(); ip4 != nil {
			space = "IPv4"
			ip = ip4
		}
	}
	return
}
