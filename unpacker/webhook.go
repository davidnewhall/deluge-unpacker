package unpacker

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"time"

	"golift.io/cnfg"
)

type WebhookConfig struct {
	Name       string          `json:"name" toml:"name" xml:"name" yaml:"name"`
	URL        string          `json:"url" toml:"url" xml:"url" yaml:"url"`
	Timeout    cnfg.Duration   `json:"timeout" toml:"timeout" xml:"timeout" yaml:"timeout"`
	IgnoreSSL  bool            `json:"ignore_ssl" toml:"ignore_ssl" xml:"ignore_ssl" yaml:"ignore_ssl"`
	Silent     bool            `json:"silent" toml:"silent" xml:"silent" yaml:"silent"`
	Events     []ExtractStatus `json:"events" toml:"events" xml:"events" yaml:"events"`
	Exclude    []string        `json:"exclude" toml:"exclude" xml:"exclude" yaml:"exclude"`
	client     *http.Client    `json:"-"`
	fails      uint            `json:"-"`
	posts      uint            `json:"-"`
	sync.Mutex `json:"-"`
}

var ErrInvalidStatus = fmt.Errorf("invalid HTTP status reply")

func (u *Unpackerr) sendWebhooks(i *Extracts) {
	for _, hook := range u.Webhook {
		if !hook.HasEvent(i.Status) || hook.Excluded(i.App) {
			continue
		}

		go func(hook *WebhookConfig) {
			if body, err := hook.Send(i); err != nil {
				u.Logf("[ERROR] Webhook: %v", err)
			} else if !hook.Silent {
				u.Logf("[Webhook] Posted Payload: %s: 200 OK", hook.Name)
				u.Debug("[DEBUG] Webhook Response: %s", string(bytes.ReplaceAll(body, []byte{'\n'}, []byte{' '})))
			}
		}(hook)
	}
}

// Sends marshals an interface{} into json and POSTs it to a URL.
func (w *WebhookConfig) Send(i interface{}) ([]byte, error) {
	w.Lock()
	defer w.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), w.Timeout.Duration+time.Second)
	defer cancel()

	b, err := w.send(ctx, i)
	if err != nil {
		w.fails++
	}

	w.posts++

	return b, err
}

func (w *WebhookConfig) send(ctx context.Context, i interface{}) ([]byte, error) {
	b, err := json.Marshal(i)
	if err != nil {
		return nil, fmt.Errorf("marshaling payload '%s': %w", w.Name, err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", w.URL, bytes.NewBuffer(b))
	if err != nil {
		return nil, fmt.Errorf("creating request '%s': %w", w.Name, err)
	}

	req.Header.Set("content-type", "application/json")

	res, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POSTing payload '%s': %w", w.Name, err)
	}
	defer res.Body.Close()

	// The error is mostly ignored because we don't care about the body.
	// Read it in to avoid a memopry leak. Used in the if-stanza below.
	body, _ := ioutil.ReadAll(res.Body)

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (%s) '%s': %s", ErrInvalidStatus, res.Status, w.Name, body)
	}

	return body, nil
}

func (u *Unpackerr) validateWebhook() {
	for i := range u.Webhook {
		if u.Webhook[i].Name == "" {
			u.Webhook[i].Name = u.Webhook[i].URL
		}

		if u.Webhook[i].Timeout.Duration == 0 {
			u.Webhook[i].Timeout.Duration = u.Timeout.Duration
		}

		if len(u.Webhook[i].Events) == 0 {
			u.Webhook[i].Events = []ExtractStatus{WAITING}
		}

		if u.Webhook[i].client == nil {
			u.Webhook[i].client = &http.Client{
				Timeout: u.Webhook[i].Timeout.Duration,
				Transport: &http.Transport{TLSClientConfig: &tls.Config{
					InsecureSkipVerify: u.Webhook[i].IgnoreSSL, // nolint: gosec
				}},
			}
		}
	}
}

func (u *Unpackerr) logWebhook() {
	if c := len(u.Webhook); c == 1 {
		u.Logf(" => Webhook Config: 1 URL: %s (timeout: %v, ignore ssl: %v, silent: %v, events: %v)",
			u.Webhook[0].Name, u.Webhook[0].Timeout, u.Webhook[0].IgnoreSSL, u.Webhook[0].Silent, logEvents(u.Webhook[0].Events))
	} else {
		u.Log(" => Webhook Configs:", c, "URLs")

		for _, f := range u.Webhook {
			u.Logf(" =>    URL: %s (timeout: %v, ignore ssl: %v, silent: %v, events: %v)",
				f.Name, f.Timeout, f.IgnoreSSL, f.Silent, logEvents(f.Events))
		}
	}
}

func logEvents(events []ExtractStatus) (s string) {
	if len(events) == 1 && events[0] == WAITING {
		return "all"
	}

	for _, e := range events {
		if len(s) > 0 {
			s += "&"
		}

		s += e.Short()
	}

	return s
}

// Excluded returns true if an app is in the Exclude slice.
func (w *WebhookConfig) Excluded(app string) bool {
	for _, a := range w.Exclude {
		if strings.EqualFold(a, app) {
			return true
		}
	}

	return false
}

// HasEvent returns true if a status event is in the Events slice.
// Also returns true if the Events slice has only one value of WAITING.
func (w *WebhookConfig) HasEvent(e ExtractStatus) bool {
	for _, h := range w.Events {
		if (h == WAITING && len(w.Events) == 1) || h == e {
			return true
		}
	}

	return false
}

// WebhookCounts returns the total count of requests and errors for all webhooks.
func (u *Unpackerr) WebhookCounts() (total uint, fails uint) {
	for _, hook := range u.Webhook {
		t, f := hook.Counts()
		total += t
		fails += f
	}

	return total, fails
}

// Counts returns the total count of requests and failures for a webhook.
func (w *WebhookConfig) Counts() (uint, uint) {
	w.Lock()
	defer w.Unlock()

	return w.posts, w.fails
}
