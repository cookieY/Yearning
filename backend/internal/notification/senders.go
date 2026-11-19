// senders.go delivery senders (PRD08 §4): SMTP email (required = mandatory STARTTLS /
// starttls = opportunistic TLS) and DingTalk robot webhook. Send failures map to structured
// failure names without carrying raw server responses.
//
// SEC-0009: outbound dialing for both sender classes goes through addrguard (validating the resolved
// IP before connect, isomorphic to AI providers) — both SMTP hosts and webhook baseURLs are
// admin-configurable targets, so the guard rejects loopback/link-local/cloud metadata
// (169.254.169.254, CGNAT 100.100.100.200, ULA fd00:ec2::254, etc.) / CGNAT / ULA / unspecified /
// broadcast / multicast / Unix sockets, defending against DNS rebinding.
package notification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// Failure names (structured and bounded; never containing raw server error strings).
const (
	FailDial       = "SMTP_DIAL_FAILED"
	FailTLS        = "SMTP_TLS_FAILED"
	FailAuth       = "SMTP_AUTH_FAILED"
	FailMail       = "SMTP_REJECTED"
	FailWebhook    = "WEBHOOK_FAILED"
	FailWebhookNOK = "WEBHOOK_NOT_OK"
	// S007 Feishu/Slack failure names (S007/RCP-20261031).
	FailFeishu    = "FEISHU_FAILED"
	FailFeishuNOK = "FEISHU_NOT_OK"
	FailSlack     = "SLACK_FAILED"
	FailSlackNOK  = "SLACK_NOT_OK"
)

// SendEmail delivers a notification email over SMTP (from -> to; secret is the SMTP password).
func SendEmail(ctx context.Context, cfg EmailConfig, secret, to, subject, body string) error {
	// SEC-0009: run shape validation first (CheckEndpoint: immediate rejection of Unix socket paths,
	// empty hosts, and disallowed IP literals + port bounds) so path-like hosts never enter slow DNS
	// resolution; resolved IPs for hostnames are guarded at dial time by the Control hook below.
	if err := addrguard.CheckEndpoint(cfg.Host, cfg.Port); err != nil {
		return fmt.Errorf("%s", FailDial)
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	// SEC-0009: outbound connection dials through addrguard; guard rejections return only bounded failure names.
	d := addrguard.GuardedDialer(10 * time.Second)
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("%s", FailDial)
	}
	deadline := time.Now().Add(15 * time.Second)
	_ = conn.SetDeadline(deadline)
	host, _, _ := net.SplitHostPort(addr)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("%s", FailDial)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			if cfg.TLSMode == "required" {
				return fmt.Errorf("%s", FailTLS)
			}
		}
	} else if cfg.TLSMode == "required" {
		return fmt.Errorf("%s", FailTLS)
	}
	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, secret, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("%s", FailAuth)
		}
	}
	if err := client.Mail(cfg.FromAddress); err != nil {
		return fmt.Errorf("%s", FailMail)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("%s", FailMail)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("%s", FailMail)
	}
	msg := buildMime(cfg.FromAddress, to, subject, body)
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("%s", FailMail)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("%s", FailMail)
	}
	if err := client.Quit(); err != nil {
		// Failures during QUIT (broken connection or abnormal server reply) likewise return only a bounded
		// failure name without leaking raw server text into last_failure_name.
		return fmt.Errorf("%s", FailMail)
	}
	return nil
}

// buildMime constructs a minimal MIME message (UTF-8 body with RFC 2047 B-encoded subject).
func buildMime(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: =?UTF-8?B?%s?=\r\n"+
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n",
		from, to, encodeB64(subject), body)
	return []byte(b.String())
}

// encodeB64 performs RFC 2047 B-encoding (standard padded base64; subjects are bounded constants
// well under the limit, so 75-char encoded-word folding is unnecessary).
func encodeB64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// dingTalkSecret JSON envelope structure for DingTalk credentials (sealed as a single AEAD envelope,
// matching the Feishu envelope posture): webhook access_token path segment + optional signing secret
// (required when robot security is set to signed mode). The configuration jsonb column stores no credentials.
type dingTalkSecret struct {
	AccessToken string `json:"access_token"`
	SignSecret  string `json:"sign_secret,omitempty"`
}

// parseDingTalkSecret parses the secret envelope: extracts fields when valid JSON with non-empty
// access_token, otherwise treats the entire string as a bare access_token (backward-compatible with existing channels).
func parseDingTalkSecret(raw string) dingTalkSecret {
	var s dingTalkSecret
	if err := json.Unmarshal([]byte(raw), &s); err == nil && s.AccessToken != "" {
		return s
	}
	return dingTalkSecret{AccessToken: raw}
}

// SendDingTalk delivers a text message via DingTalk robot webhook (secret = bare access_token or
// {access_token,sign_secret} envelope; attaches timestamp+sign per official algorithm when signed.
// Never logged or emitted in events). Returns a structured failure name. SEC-0009: outbound requests
// dial through addrguard (baseURL is admin-configurable, sharing the same posture as SMTP).
func SendDingTalk(ctx context.Context, baseURL, secret, text string) error {
	if baseURL == "" {
		baseURL = "https://oapi.dingtalk.com"
	}
	s := parseDingTalkSecret(secret)
	url := baseURL + "/robot/send?access_token=" + neturl.QueryEscape(s.AccessToken)
	if s.SignSecret != "" {
		// Official signing algorithm: HMAC-SHA256(key=signSecret, data=millisTimestamp + "\n" + signSecret),
		// base64-encoded and URL-encoded, appended to the webhook URL as timestamp and sign query parameters.
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		mac := hmac.New(sha256.New, []byte(s.SignSecret))
		mac.Write([]byte(timestamp + "\n" + s.SignSecret))
		sign := neturl.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		url += "&timestamp=" + timestamp + "&sign=" + sign
	}
	payload := fmt.Sprintf(`{"msgtype":"text","text":{"content":%q}}`, text)
	return postWebhook(ctx, url, []byte(payload), FailWebhook, FailWebhookNOK, nil)
}

// feishuSecret JSON envelope structure for Feishu credentials (sealed as a single AEAD envelope;
// configuration jsonb stores no credentials — both the token-bearing webhook URL and signing secret are treated as sensitive).
type feishuSecret struct {
	WebhookURL string `json:"webhook_url"`
	SignSecret string `json:"sign_secret,omitempty"`
}

// SendFeishu delivers a text message via custom robot webhook (official v2 hook API:
// HMAC-SHA256(key=timestamp+"\n"+secret) over an empty byte slice, base64-encoded; success =
// HTTP 200 with code=0 — checking the business code prevents reporting HTTP 200 error payloads as sent).
// Outbound dialing goes through addrguard (same SEC-0009 posture).
func SendFeishu(ctx context.Context, secretJSON, text string) error {
	var s feishuSecret
	if err := json.Unmarshal([]byte(secretJSON), &s); err != nil || s.WebhookURL == "" {
		return fmt.Errorf("%s", FailFeishu)
	}
	body := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	}
	if s.SignSecret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(ts+"\n"+s.SignSecret))
		body["timestamp"] = ts
		body["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s", FailFeishu)
	}
	return postWebhookExpectBusinessCode(ctx, s.WebhookURL, payload, FailFeishu, FailFeishuNOK)
}

// SendSlack delivers a text message via Incoming Webhook (full URL = Secret; success =
// HTTP 200 with body "ok"; 400/403/404 responses map to bounded NOK failure names without echoing response bodies).
func SendSlack(ctx context.Context, webhookURL, text string) error {
	if webhookURL == "" {
		return fmt.Errorf("%s", FailSlack)
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("%s", FailSlack)
	}
	return postWebhook(ctx, webhookURL, payload, FailSlack, FailSlackNOK, func(resp *http.Response) error {
		ok, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
		if strings.TrimSpace(string(ok)) != "ok" {
			return fmt.Errorf("%s", FailSlackNOK)
		}
		return nil
	})
}

// webhookClient constructs an HTTP client dialing through addrguard (shared across all IM webhook senders).
func webhookClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: addrguard.DialFunc(10 * time.Second),
		},
	}
}

// postWebhook performs a generic webhook POST (checking status code and optional body; failures return only bounded names).
func postWebhook(ctx context.Context, url string, payload []byte, failName, nokName string, bodyCheck func(*http.Response) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%s", failName)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := webhookClient().Do(req)
	if err != nil {
		return fmt.Errorf("%s", failName)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", nokName)
	}
	if bodyCheck != nil {
		return bodyCheck(resp)
	}
	return nil
}

// postWebhookExpectBusinessCode handles Feishu responses: HTTP 200 with business code != 0 is still a failure
// (acceptance-14: platform HTTP 200 carrying a business error must never be misreported as sent).
func postWebhookExpectBusinessCode(ctx context.Context, url string, payload []byte, failName, nokName string) error {
	return postWebhook(ctx, url, payload, failName, nokName, func(resp *http.Response) error {
		var body struct {
			Code int `json:"code"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(data, &body) != nil || body.Code != 0 {
			return fmt.Errorf("%s", nokName)
		}
		return nil
	})
}
