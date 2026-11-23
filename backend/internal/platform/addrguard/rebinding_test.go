// rebinding_test.go verifies DNS rebinding defense over a real local UDP DNS stub (RFC 1035 wire format):
//  1. First lookup (preflight ValidateHostFor) sees an allowed public IP (192.0.2.10 / 2001:db8::10, TTL=0).
//  2. Second lookup (dial-time DialFuncFor Control hook) receives a forbidden IP (127.0.0.1 / ::1),
//     and Control blocks the TCP connect before the decoy listener accepts a single socket.
//  3. Mixed A + AAAA records where either address is forbidden are rejected at both preflight and ResolvePinned.
package addrguard

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type rebindDNSKey struct {
	name  string
	qtype dnsmessage.Type
}

type stubDNSServer struct {
	pc      net.PacketConn
	addr    string
	mu      sync.Mutex
	answers map[rebindDNSKey][][]net.IP
	counts  map[rebindDNSKey]int
}

func startStubDNS(t *testing.T, answers map[rebindDNSKey][][]net.IP) *stubDNSServer {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp dns stub: %v", err)
	}
	s := &stubDNSServer{
		pc:      pc,
		addr:    pc.LocalAddr().String(),
		answers: answers,
		counts:  make(map[rebindDNSKey]int),
	}
	t.Cleanup(func() { _ = pc.Close() })
	go s.serve()
	return s
}

func (s *stubDNSServer) serve() {
	buf := make([]byte, 1500)
	for {
		n, peer, err := s.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		var msg dnsmessage.Message
		if err := msg.Unpack(buf[:n]); err != nil || len(msg.Questions) == 0 {
			continue
		}
		q := msg.Questions[0]
		key := rebindDNSKey{name: q.Name.String(), qtype: q.Type}

		s.mu.Lock()
		seq := s.answers[key]
		call := s.counts[key]
		var chosen []net.IP
		if len(seq) > 0 {
			idx := call
			if idx >= len(seq) {
				idx = len(seq) - 1
			}
			chosen = seq[idx]
			s.counts[key] = call + 1
		}
		s.mu.Unlock()

		resp := dnsmessage.Message{
			Header: dnsmessage.Header{
				ID:                 msg.ID,
				Response:           true,
				Authoritative:      true,
				RecursionAvailable: true,
				RCode:              dnsmessage.RCodeSuccess,
			},
			Questions: []dnsmessage.Question{q},
		}
		for _, ip := range chosen {
			switch q.Type {
			case dnsmessage.TypeA:
				if v4 := ip.To4(); v4 != nil {
					var b [4]byte
					copy(b[:], v4)
					resp.Answers = append(resp.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{
							Name:  q.Name,
							Type:  dnsmessage.TypeA,
							Class: dnsmessage.ClassINET,
							TTL:   0,
						},
						Body: &dnsmessage.AResource{A: b},
					})
				}
			case dnsmessage.TypeAAAA:
				if v16 := ip.To16(); v16 != nil && ip.To4() == nil {
					var b [16]byte
					copy(b[:], v16)
					resp.Answers = append(resp.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{
							Name:  q.Name,
							Type:  dnsmessage.TypeAAAA,
							Class: dnsmessage.ClassINET,
							TTL:   0,
						},
						Body: &dnsmessage.AAAAResource{AAAA: b},
					})
				}
			}
		}
		wire, err := resp.Pack()
		if err != nil {
			continue
		}
		_, _ = s.pc.WriteTo(wire, peer)
	}
}

func (s *stubDNSServer) netResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", s.addr)
		},
	}
}

func (s *stubDNSServer) resolverFunc(nr *net.Resolver) Resolver {
	return func(ctx context.Context, host string) ([]net.IP, error) {
		addrs, err := nr.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		out := make([]net.IP, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, a.IP)
		}
		return out, nil
	}
}

func TestDNSRebindingIPv4BlockedAtDialTime(t *testing.T) {
	bypassOff(t)
	t.Cleanup(SetAllowLoopbackDatasourceForTest(false))

	decoy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = decoy.Close() }()
	var decoyAccepts atomic.Int32
	go func() {
		for {
			c, err := decoy.Accept()
			if err != nil {
				return
			}
			decoyAccepts.Add(1)
			_ = c.Close()
		}
	}()
	_, decoyPort, err := net.SplitHostPort(decoy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	stub := startStubDNS(t, map[rebindDNSKey][][]net.IP{
		{name: "rebind-v4.test.", qtype: dnsmessage.TypeA}: {
			{mustIP(t, "192.0.2.10")}, // Preflight resolution sees allowed TEST-NET-1
			{mustIP(t, "127.0.0.1")},  // Dial-time resolution rebinds to loopback
		},
	})
	nr := stub.netResolver()
	t.Cleanup(SetDialerResolverForTest(nr))

	if err := ValidateHostFor(context.Background(), PurposeWebhook, "rebind-v4.test", stub.resolverFunc(nr)); err != nil {
		t.Fatalf("preflight ValidateHostFor = %v; want allowed before rebind", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, derr := DialFuncFor(PurposeWebhook, time.Second)(ctx, "tcp", net.JoinHostPort("rebind-v4.test", decoyPort))
	if derr == nil {
		_ = conn.Close()
		t.Fatal("rebound dial succeeded; want ErrLoopbackForbidden")
	}
	if !errors.Is(derr, ErrLoopbackForbidden) || !errors.Is(derr, ErrAddressForbidden) {
		t.Fatalf("rebound dial error = %v; want ErrLoopbackForbidden", derr)
	}
	if got := decoyAccepts.Load(); got != 0 {
		t.Fatalf("decoy listener accepted %d connections; want 0", got)
	}
}

func TestDNSRebindingIPv6BlockedAtDialTime(t *testing.T) {
	bypassOff(t)
	t.Cleanup(SetAllowLoopbackDatasourceForTest(false))

	decoy, err := net.Listen("tcp6", "[::1]:0")
	var decoyPort = "15432"
	var decoyAccepts atomic.Int32
	if err == nil {
		defer func() { _ = decoy.Close() }()
		_, decoyPort, _ = net.SplitHostPort(decoy.Addr().String())
		go func() {
			for {
				c, aerr := decoy.Accept()
				if aerr != nil {
					return
				}
				decoyAccepts.Add(1)
				_ = c.Close()
			}
		}()
	}

	stub := startStubDNS(t, map[rebindDNSKey][][]net.IP{
		{name: "rebind-v6.test.", qtype: dnsmessage.TypeAAAA}: {
			{mustIP(t, "2001:db8::10")}, // Preflight sees public unicast
			{mustIP(t, "::1")},          // Dial-time rebinds to IPv6 loopback
		},
	})
	nr := stub.netResolver()
	t.Cleanup(SetDialerResolverForTest(nr))

	if err := ValidateHostFor(context.Background(), PurposeWebhook, "rebind-v6.test", stub.resolverFunc(nr)); err != nil {
		t.Fatalf("preflight ValidateHostFor IPv6 = %v; want allowed before rebind", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, derr := DialFuncFor(PurposeWebhook, time.Second)(ctx, "tcp", net.JoinHostPort("rebind-v6.test", decoyPort))
	if derr == nil {
		_ = conn.Close()
		t.Fatal("rebound IPv6 dial succeeded; want ErrLoopbackForbidden")
	}
	if !errors.Is(derr, ErrLoopbackForbidden) {
		t.Fatalf("rebound IPv6 dial error = %v; want ErrLoopbackForbidden", derr)
	}
	if got := decoyAccepts.Load(); got != 0 {
		t.Fatalf("decoy IPv6 listener accepted %d connections; want 0", got)
	}
}

func TestMixedAAAAAndAWithOneForbiddenRecordRejected(t *testing.T) {
	bypassOff(t)
	stub := startStubDNS(t, map[rebindDNSKey][][]net.IP{
		{name: "mixed.test.", qtype: dnsmessage.TypeA}: {
			{mustIP(t, "192.0.2.10")},
		},
		{name: "mixed.test.", qtype: dnsmessage.TypeAAAA}: {
			{mustIP(t, "::1")},
		},
	})
	nr := stub.netResolver()
	rf := stub.resolverFunc(nr)
	if err := ValidateHostFor(context.Background(), PurposeWebhook, "mixed.test", rf); !errors.Is(err, ErrLoopbackForbidden) {
		t.Fatalf("ValidateHostFor mixed A+AAAA = %v; want ErrLoopbackForbidden", err)
	}
	if _, err := ResolvePinned(context.Background(), PurposeOSC, "mixed.test", 3306, "", rf); !errors.Is(err, ErrLoopbackForbidden) {
		t.Fatalf("ResolvePinned mixed A+AAAA = %v; want ErrLoopbackForbidden", err)
	}
}
