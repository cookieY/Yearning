// senders_test.go unit tests for sender egress guards (SEC-0009): both SMTP and DingTalk webhook
// dialing go through addrguard — forbidden targets (loopback/link-local/cloud metadata/CGNAT/ULA)
// fail in the Control hook before making any network connection; allowed paths are tested end-to-end
// using the loopback bypass + minimal local SMTP/HTTP stubs, and public/RFC1918 control cases are
// tested directly via addrguard (the same ValidateIP gate invoked by the Control hook).
package notification

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// bypassOff forces the loopback bypass off during a test (matching addrguard package tests:
// the process environment might carry the bypass variable, so each test pins default-deny semantics explicitly).
func bypassOff(t *testing.T) {
	t.Helper()
	t.Cleanup(addrguard.SetLoopbackBypassForTest(false))
	addrguard.SetLoopbackBypassForTest(false)
}

// TestSendEmailRejectsForbiddenTargets verifies the guard deny surface: guarded dialing rejects
// before connect, returning a bounded failure name within seconds even when targets are unreachable.
func TestSendEmailRejectsForbiddenTargets(t *testing.T) {
	bypassOff(t)
	for _, host := range []string{
		"127.0.0.1",          // loopback
		"169.254.169.254",    // Cloud metadata (link-local)
		"100.100.100.200",    // Alibaba Cloud metadata (CGNAT range)
		"fd00:ec2::254",      // AWS IMDSv6 (ULA range)
		"0.0.0.0",            // unspecified
		"/var/run/mail.sock", // Unix socket path form
	} {
		cfg := EmailConfig{Host: host, Port: 25, FromAddress: "noreply@example.com"}
		start := time.Now()
		err := SendEmail(context.Background(), cfg, "secret", "to@example.com", "s", "b")
		if err == nil || err.Error() != FailDial {
			t.Errorf("SendEmail(%s) = %v; want %s", host, err, FailDial)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("SendEmail(%s) took %v; guard rejection must be immediate", host, time.Since(start))
		}
	}
}

// TestSendEmailAllowPathWithBypass verifies the allowed path end-to-end: when the explicit dev bypass
// is active, loopback targets are permitted and the minimal SMTP stub completes the full flow
// (no STARTTLS advertised under opportunistic starttls -> DATA delivery -> QUIT).
func TestSendEmailAllowPathWithBypass(t *testing.T) {
	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go serveStubSMTP(t, ln)

	cfg := EmailConfig{
		Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port,
		FromAddress: "noreply@example.com",
	}
	if err := SendEmail(context.Background(), cfg, "secret", "to@example.com", "hello", "body"); err != nil {
		t.Fatalf("SendEmail through bypass + stub server = %v; want nil", err)
	}

	if host, addr := smtpDialAddr(" fd00::25 ", 25); host != "fd00::25" || addr != "[fd00::25]:25" {
		t.Fatalf("smtpDialAddr unbracketed IPv6 = (%q, %q); want (\"fd00::25\", \"[fd00::25]:25\")", host, addr)
	}
	if host, addr := smtpDialAddr("[fd00::25]", 587); host != "fd00::25" || addr != "[fd00::25]:587" {
		t.Fatalf("smtpDialAddr bracketed IPv6 = (%q, %q); want (\"fd00::25\", \"[fd00::25]:587\")", host, addr)
	}
	if ln6, err := net.Listen("tcp6", "[::1]:0"); err == nil {
		t.Cleanup(func() { _ = ln6.Close() })
		go serveStubSMTP(t, ln6)
		cfg6 := EmailConfig{
			Host: "::1", Port: ln6.Addr().(*net.TCPAddr).Port,
			FromAddress: "noreply@example.com",
		}
		if err := SendEmail(context.Background(), cfg6, "secret", "to@example.com", "hello6", "body6"); err != nil {
			t.Fatalf("SendEmail unbracketed IPv6 loopback = %v; want nil", err)
		}
	}
}

// serveStubSMTP runs a minimal SMTP dialog: plain greeting without extensions (client does not attempt STARTTLS),
// 250 replies per command, 354 -> dot terminator -> 250 for DATA, and 221 on QUIT before closing.
func serveStubSMTP(t *testing.T, ln net.Listener) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	reply := func(s string) {
		_, _ = conn.Write([]byte(s + "\r\n"))
	}
	reply("220 stub ESMTP")
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if inData {
			if trimmed == "." {
				inData = false
				reply("250 accepted")
			}
			continue
		}
		switch {
		case strings.HasPrefix(strings.ToUpper(trimmed), "EHLO"),
			strings.HasPrefix(strings.ToUpper(trimmed), "HELO"):
			reply("250 stub")
		case strings.HasPrefix(strings.ToUpper(trimmed), "MAIL FROM"):
			reply("250 ok")
		case strings.HasPrefix(strings.ToUpper(trimmed), "RCPT TO"):
			reply("250 ok")
		case strings.HasPrefix(strings.ToUpper(trimmed), "DATA"):
			reply("354 go ahead")
			inData = true
		case strings.HasPrefix(strings.ToUpper(trimmed), "QUIT"):
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

type stubSMTPOptions struct {
	allowSTARTTLS bool
	tlsConfig     *tls.Config
	tlsActiveInit bool
	tlsCompleted  *atomic.Bool
	authOK        *atomic.Bool
	delivered     *atomic.Bool
}

func newSelfSignedSMTPServerCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func serveStubSMTPConn(conn net.Conn, opts stubSMTPOptions) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	activeConn := conn
	r := bufio.NewReader(activeConn)
	reply := func(s string) {
		_, _ = activeConn.Write([]byte(s + "\r\n"))
	}
	reply("220 stub ESMTP")
	inData := false
	tlsActive := opts.tlsActiveInit
	if tlsActive && opts.tlsCompleted != nil {
		opts.tlsCompleted.Store(true)
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if inData {
			if trimmed == "." {
				inData = false
				if opts.delivered != nil {
					opts.delivered.Store(true)
				}
				reply("250 accepted")
			}
			continue
		}
		upper := strings.ToUpper(trimmed)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			reply("250-stub")
			if opts.allowSTARTTLS && !tlsActive {
				reply("250-STARTTLS")
			}
			reply("250 AUTH PLAIN")
		case upper == "STARTTLS" && opts.allowSTARTTLS && opts.tlsConfig != nil:
			reply("220 Ready to start TLS")
			tlsConn := tls.Server(activeConn, opts.tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			activeConn = tlsConn
			r = bufio.NewReader(activeConn)
			tlsActive = true
			if opts.tlsCompleted != nil {
				opts.tlsCompleted.Store(true)
			}
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			if opts.authOK != nil {
				opts.authOK.Store(true)
			}
			reply("235 ok")
		case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RCPT TO"):
			reply("250 ok")
		case upper == "DATA":
			reply("354 go ahead")
			inData = true
		case upper == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func TestSendEmailImplicitTLS(t *testing.T) {
	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)

	cert, pool := newSelfSignedSMTPServerCert(t)
	srvTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srvTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var tlsCompleted, authOK, delivered atomic.Bool
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveStubSMTPConn(c, stubSMTPOptions{
				tlsActiveInit: true,
				tlsCompleted:  &tlsCompleted,
				authOK:        &authOK,
				delivered:     &delivered,
			})
		}
	}()

	cfg := EmailConfig{
		Host:        "127.0.0.1",
		Port:        ln.Addr().(*net.TCPAddr).Port,
		TLSMode:     "required",
		Username:    "sender",
		FromAddress: "sender@example.com",
	}
	if err := sendEmailWithTLS(context.Background(), cfg, "app-pass", "to@example.com", "hello", "body", &tls.Config{RootCAs: pool}); err != nil {
		t.Fatalf("sendEmailWithTLS implicit TLS = %v; want nil", err)
	}
	if !tlsCompleted.Load() || !authOK.Load() || !delivered.Load() {
		t.Fatalf("expected TLS+AUTH+DATA completion, got tls=%v auth=%v delivered=%v", tlsCompleted.Load(), authOK.Load(), delivered.Load())
	}

	if err := SendEmail(context.Background(), cfg, "app-pass", "to@example.com", "hello", "body"); err == nil || err.Error() != FailTLS {
		t.Fatalf("SendEmail implicit TLS untrusted cert = %v; want %s", err, FailTLS)
	}

	plainLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plainLn.Close() })
	go serveStubSMTP(t, plainLn)

	plainCfg := EmailConfig{
		Host:        "127.0.0.1",
		Port:        plainLn.Addr().(*net.TCPAddr).Port,
		TLSMode:     "required",
		FromAddress: "sender@example.com",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sendEmailWithTLS(ctx, plainCfg, "app-pass", "to@example.com", "hello", "body", &tls.Config{RootCAs: pool}); err == nil || err.Error() != FailTLS {
		t.Fatalf("sendEmailWithTLS required against plaintext server = %v; want %s", err, FailTLS)
	}
}

func TestSendEmailSTARTTLS(t *testing.T) {
	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)

	cert, pool := newSelfSignedSMTPServerCert(t)
	srvTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var tlsCompleted, authOK, delivered atomic.Bool
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveStubSMTPConn(c, stubSMTPOptions{
				allowSTARTTLS: true,
				tlsConfig:     srvTLS,
				tlsCompleted:  &tlsCompleted,
				authOK:        &authOK,
				delivered:     &delivered,
			})
		}
	}()

	cfg := EmailConfig{
		Host:        "127.0.0.1",
		Port:        ln.Addr().(*net.TCPAddr).Port,
		TLSMode:     "starttls",
		Username:    "sender",
		FromAddress: "sender@example.com",
	}
	if err := sendEmailWithTLS(context.Background(), cfg, "app-pass", "to@example.com", "hello", "body", &tls.Config{RootCAs: pool}); err != nil {
		t.Fatalf("sendEmailWithTLS STARTTLS = %v; want nil", err)
	}
	if !tlsCompleted.Load() || !authOK.Load() || !delivered.Load() {
		t.Fatalf("expected STARTTLS+AUTH+DATA completion, got tls=%v auth=%v delivered=%v", tlsCompleted.Load(), authOK.Load(), delivered.Load())
	}

	if err := SendEmail(context.Background(), cfg, "app-pass", "to@example.com", "hello", "body"); err == nil || err.Error() != FailTLS {
		t.Fatalf("SendEmail STARTTLS untrusted cert = %v; want %s", err, FailTLS)
	}
}

// TestSendDingTalkRejectsForbiddenTargets verifies the webhook egress guard deny surface
// (baseURL is admin-configurable and shares the same guard semantics as SMTP).
func TestSendDingTalkRejectsForbiddenTargets(t *testing.T) {
	bypassOff(t)
	for _, baseURL := range []string{
		"http://127.0.0.1:1",
		"http://169.254.169.254:80",
		"http://100.100.100.200:80",
		"http://[fd00:ec2::254]:80",
	} {
		start := time.Now()
		err := SendDingTalk(context.Background(), baseURL, "token", "hello")
		if err == nil || err.Error() != FailWebhook {
			t.Errorf("SendDingTalk(%s) = %v; want %s", baseURL, err, FailWebhook)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("SendDingTalk(%s) took %v; guard rejection must be immediate", baseURL, time.Since(start))
		}
	}
}

// TestSendDingTalkAllowPathWithBypass verifies the webhook allow path: when bypass is active,
// the loopback httptest server is reachable and non-200 responses map to the bounded NOK failure name.
func TestSendDingTalkAllowPathWithBypass(t *testing.T) {
	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	if err := SendDingTalk(context.Background(), srv.URL, "token", "hello"); err != nil {
		t.Fatalf("SendDingTalk through bypass + stub = %v; want nil", err)
	}

	nok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(nok.Close)
	if err := SendDingTalk(context.Background(), nok.URL, "token", "hello"); err == nil || err.Error() != FailWebhookNOK {
		t.Fatalf("SendDingTalk non-200 = %v; want %s", err, FailWebhookNOK)
	}
}

// TestSendDingTalkSignsEnvelopeRequests verifies signed envelopes (owner acceptance 2026-10-31 round 2):
// timestamp+sign computed per the official algorithm are appended to the webhook query string; unsigned
// envelopes and bare tokens omit them; signatures are verified by recomputing from the captured timestamp.
func TestSendDingTalkSignsEnvelopeRequests(t *testing.T) {
	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)

	var gotQuery neturl.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = *r.URL
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// Envelope + signing secret.
	envelope := `{"access_token":"token123","sign_secret":"SECxyz"}`
	if err := SendDingTalk(context.Background(), srv.URL, envelope, "hello"); err != nil {
		t.Fatalf("SendDingTalk signed envelope = %v; want nil", err)
	}
	q := gotQuery.Query()
	if q.Get("access_token") != "token123" {
		t.Fatalf("access_token = %q; want token123", q.Get("access_token"))
	}
	ts, sign := q.Get("timestamp"), q.Get("sign")
	if ts == "" || sign == "" {
		t.Fatalf("signed request missing timestamp/sign: %v", q)
	}
	mac := hmac.New(sha256.New, []byte("SECxyz"))
	mac.Write([]byte(ts + "\n" + "SECxyz"))
	if want := base64.StdEncoding.EncodeToString(mac.Sum(nil)); sign != want {
		t.Fatalf("sign = %q; want %q", sign, want)
	}

	// Unsigned envelope: must not attach timestamp/sign.
	if err := SendDingTalk(context.Background(), srv.URL, `{"access_token":"tok2"}`, "hello"); err != nil {
		t.Fatalf("SendDingTalk unsigned envelope = %v; want nil", err)
	}
	q = gotQuery.Query()
	if q.Get("access_token") != "tok2" || q.Get("timestamp") != "" || q.Get("sign") != "" {
		t.Fatalf("unsigned envelope query = %v", q)
	}

	// Bare token fallback (legacy channel format).
	if err := SendDingTalk(context.Background(), srv.URL, "rawtoken", "hello"); err != nil {
		t.Fatalf("SendDingTalk raw token = %v; want nil", err)
	}
	if q := gotQuery.Query(); q.Get("access_token") != "rawtoken" || q.Get("sign") != "" {
		t.Fatalf("raw token query = %v", q)
	}
}

// TestGuardAllowsRFC1918AndPublic verifies the control surface (direct addrguard test): the
// GuardedDialer Control hook calls addrguard.ValidateIP — RFC1918 private networks and public
// unicast addresses are allowed (enterprise mail gateways / public SaaS webhooks), while loopback is
// allowed only when the explicit dev bypass is enabled.
func TestGuardAllowsRFC1918AndPublic(t *testing.T) {
	bypassOff(t)
	for _, ip := range []string{
		"10.0.0.5", "172.16.9.9", "192.168.1.25", // RFC1918 (common for internal mail gateways)
		"8.8.8.8", "203.0.113.10", "2001:db8::25", // Public unicast
	} {
		if err := addrguard.ValidateIP(mustParseIP(ip)); err != nil {
			t.Errorf("ValidateIP(%s) = %v; want allowed", ip, err)
		}
	}
}

func mustParseIP(s string) net.IP {
	ip := net.ParseIP(s)
	if ip == nil {
		panic("bad test IP " + s)
	}
	return ip
}

// ---- S007: Feishu/Slack senders and Secret shape validation ----

// TestSendFeishuSignAndBusinessCode verifies the official Feishu v2 hook protocol: HMAC signature
// fields + HTTP 200 with code != 0 must be treated as a failure (acceptance-14).
func TestSendFeishuSignAndBusinessCode(t *testing.T) {
	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)
	var gotBody []byte
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		gotBody = b[:n]
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer okSrv.Close()
	nokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":19021,"msg":"sign match fail"}`))
	}))
	defer nokSrv.Close()

	secret := `{"webhook_url":"` + okSrv.URL + `","sign_secret":"s1"}`
	if err := SendFeishu(context.Background(), secret, "hello"); err != nil {
		t.Fatalf("feishu success path failed: %v", err)
	}
	if !strings.Contains(string(gotBody), `"msg_type":"text"`) || !strings.Contains(string(gotBody), `"sign"`) {
		t.Fatalf("feishu payload missing text/sign: %s", gotBody)
	}
	if err := SendFeishu(context.Background(), `{"webhook_url":"`+nokSrv.URL+`"}`, "hello"); err == nil ||
		err.Error() != FailFeishuNOK {
		t.Fatalf("feishu business-error must be %s, got %v", FailFeishuNOK, err)
	}
}

// TestSendSlackOKBody verifies Slack Incoming Webhook handling: success = HTTP 200 with body "ok";
// non-ok bodies and unreachable addresses map to bounded failure names.
func TestSendSlackOKBody(t *testing.T) {
	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer okSrv.Close()
	nokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"channel_is_archived"}`))
	}))
	defer nokSrv.Close()

	if err := SendSlack(context.Background(), okSrv.URL, "hello"); err != nil {
		t.Fatalf("slack success path failed: %v", err)
	}
	if err := SendSlack(context.Background(), nokSrv.URL, "hello"); err == nil ||
		err.Error() != FailSlackNOK {
		t.Fatalf("slack non-ok must be %s, got %v", FailSlackNOK, err)
	}
	if err := SendSlack(context.Background(), "", "hello"); err == nil || err.Error() != FailSlack {
		t.Fatalf("empty webhook must be %s, got %v", FailSlack, err)
	}
}

// TestValidateSecretValue verifies S007 channel credential shape validation: pinned official hosts,
// token character allowlist, rejection of injection patterns, and never echoing credential values.
func TestValidateSecretValue(t *testing.T) {
	cases := []struct {
		kind, value string
		ok          bool
	}{
		{"feishu", `{"webhook_url":"https://open.feishu.cn/open-apis/bot/v2/hook/abc-123"}`, true},
		{"feishu", `{"webhook_url":"https://open.feishu.cn/open-apis/bot/v2/hook/abc?x=1"}`, false},
		{"feishu", `{"webhook_url":"https://evil.example/hook/abc"}`, false},
		{"feishu", `{"webhook_url":""}`, false},
		{"feishu", "not-json", false},
		{"slack", "https://hooks.slack.com/services/T000/B000/xxxx", true},
		{"slack", "https://hooks.slack.com/services/T000/B000/xx xx", false},
		{"slack", "http://hooks.slack.com/services/T000/B000/xxxx", false},
		{"slack", "https://evil.example/services/T/B/x", false},
		{"email", "any-password-material", true},
		{"dingtalk", "token-path-segment", true},
		{"dingtalk", `{"access_token":"abc123","sign_secret":"SECxyz"}`, true},
		{"dingtalk", `{"access_token":"abc123"}`, true},
		{"dingtalk", `{"access_token":"","sign_secret":"SECxyz"}`, false},
		{"dingtalk", `{"access_token":"abc?x"}`, false},
		{"dingtalk", `{"access_token":"abc","sign_secret":"SEC x"}`, false},
	}
	for _, c := range cases {
		err := validateSecretValue(c.kind, c.value)
		if c.ok && err != nil {
			t.Fatalf("%s %q: unexpected rejection %v", c.kind, c.value, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s %q: expected rejection", c.kind, c.value)
		}
	}
}

// TestMaskAddress verifies preview email masking: retains <= 2 chars of local part + *** without exposing full addresses.
func TestMaskAddress(t *testing.T) {
	if got := maskAddress("operator@corp.example"); got != "op***@corp.example" {
		t.Fatalf("mask = %q", got)
	}
	if got := maskAddress("ab@corp.example"); got != "ab***@corp.example" {
		t.Fatalf("mask = %q", got)
	}
	if got := maskAddress("no-at-sign"); got != "***" {
		t.Fatalf("mask = %q", got)
	}
}
