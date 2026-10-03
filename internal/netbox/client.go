// Package netbox is a small client for the parts of the NetBox REST API the
// agent uses. Supported NetBox versions: 4.0 and later.
package netbox

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/frauniki/netbox-groundtruth-agent/internal/config"
)

// MinVersion is the oldest NetBox release the agent is written for.
var MinVersion = Version{4, 0}

// macObjectsVersion is the release that made MAC addresses separate objects.
var macObjectsVersion = Version{4, 2}

type Version struct{ Major, Minor int }

func (v Version) Less(o Version) bool {
	return v.Major < o.Major || (v.Major == o.Major && v.Minor < o.Minor)
}

func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

type Client struct {
	base    string
	http    *http.Client
	Version Version
}

// New builds a client from cfg. It does not contact NetBox; call Init.
func New(cfg config.NetBox, token string) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("netbox.url is not set")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates in %s", cfg.CAFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	// NetBox v2 tokens ("nbt_...") use Bearer, legacy v1 tokens use Token.
	auth := "Token " + token
	if strings.HasPrefix(token, "nbt_") {
		auth = "Bearer " + token
	}
	headers := map[string]string{"Authorization": auth, "Accept": "application/json"}
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	var rt http.RoundTripper = headerTransport{tr, headers}
	switch cfg.Auth {
	case "", "direct":
	case "iap":
		var err error
		if rt, err = iapTransport(rt, cfg.IAP); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown netbox.auth %q", cfg.Auth)
	}
	return &Client{
		base: strings.TrimSuffix(cfg.URL, "/"),
		http: &http.Client{Transport: rt, Timeout: cfg.Timeout},
	}, nil
}

type headerTransport struct {
	next    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.next.RoundTrip(r)
}

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)`)

// Init reads /api/status/ and checks the NetBox version.
func (c *Client) Init(ctx context.Context) error {
	var st map[string]any
	if err := c.Do(ctx, http.MethodGet, "/api/status/", nil, &st); err != nil {
		return err
	}
	raw, _ := st["netbox-version"].(string)
	m := versionRe.FindStringSubmatch(raw)
	if m == nil {
		return fmt.Errorf("unexpected netbox-version %q in /api/status/", raw)
	}
	c.Version.Major, _ = strconv.Atoi(m[1])
	c.Version.Minor, _ = strconv.Atoi(m[2])
	if c.Version.Less(MinVersion) {
		return fmt.Errorf("NetBox %s is not supported (need %s or later)", c.Version, MinVersion)
	}
	return nil
}

// MACObjects reports whether MAC addresses are separate objects (NetBox 4.2+).
func (c *Client) MACObjects() bool { return !c.Version.Less(macObjectsVersion) }

// Do sends a request and decodes the JSON response into out (if non-nil).
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, truncate(string(b), 500))
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// list fetches every page of a list endpoint. It fails unless the number of
// results equals the reported count, so a truncated or malformed answer is
// never mistaken for "these objects do not exist".
func list[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	var all []T
	for {
		q.Set("limit", "1000")
		q.Set("offset", strconv.Itoa(len(all)))
		var page struct {
			Count   *int `json:"count"`
			Results []T  `json:"results"`
		}
		if err := c.Do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		if page.Count == nil || page.Results == nil {
			return nil, fmt.Errorf("GET %s: response is not a paginated list", path)
		}
		all = append(all, page.Results...)
		if len(all) == *page.Count {
			return all, nil
		}
		if len(page.Results) == 0 || len(all) > *page.Count {
			return nil, fmt.Errorf("GET %s: got %d results, expected %d", path, len(all), *page.Count)
		}
	}
}

// Ref is a nested object reference.
type Ref struct {
	ID   int    `json:"id"`
	Name string `json:"name,omitempty"`
	Slug string `json:"slug,omitempty"`
}

type Device struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Serial     string `json:"serial"`
	DeviceType struct {
		Model        string `json:"model"`
		Manufacturer Ref    `json:"manufacturer"`
	} `json:"device_type"`
	// CustomFields holds every custom field defined for devices, set or not.
	CustomFields map[string]any `json:"custom_fields"`
}

type Interface struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	MACAddress string `json:"mac_address"`
	// MACAddresses is present on NetBox 4.2+.
	MACAddresses []struct {
		ID         int    `json:"id"`
		MACAddress string `json:"mac_address"`
	} `json:"mac_addresses"`
}

// MACs returns every MAC address known for the interface, upper case.
func (i Interface) MACs() []string {
	var out []string
	if i.MACAddress != "" {
		out = append(out, strings.ToUpper(i.MACAddress))
	}
	for _, m := range i.MACAddresses {
		out = append(out, strings.ToUpper(m.MACAddress))
	}
	return out
}

type InventoryItem struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	PartID      string `json:"part_id"`
	Serial      string `json:"serial"`
	Description string `json:"description"`
	Discovered  bool   `json:"discovered"`
	Tags        []Ref  `json:"tags"`
}

func (i InventoryItem) HasTag(slug string) bool {
	return slices.ContainsFunc(i.Tags, func(t Ref) bool { return t.Slug == slug })
}

type IPAddress struct {
	ID      int    `json:"id"`
	Address string `json:"address"`
}

func (c *Client) DevicesBySerial(ctx context.Context, serial string) ([]Device, error) {
	return list[Device](ctx, c, "/api/dcim/devices/", url.Values{"serial": {serial}})
}

func (c *Client) Interfaces(ctx context.Context, deviceID int) ([]Interface, error) {
	return list[Interface](ctx, c, "/api/dcim/interfaces/", url.Values{"device_id": {strconv.Itoa(deviceID)}})
}

func (c *Client) InventoryItems(ctx context.Context, deviceID int) ([]InventoryItem, error) {
	return list[InventoryItem](ctx, c, "/api/dcim/inventory-items/", url.Values{"device_id": {strconv.Itoa(deviceID)}})
}

func (c *Client) IPAddresses(ctx context.Context, deviceID int) ([]IPAddress, error) {
	return list[IPAddress](ctx, c, "/api/ipam/ip-addresses/", url.Values{"device_id": {strconv.Itoa(deviceID)}})
}

// TagID returns the ID of the tag with slug, or 0 if it does not exist.
func (c *Client) TagID(ctx context.Context, slug string) (int, error) {
	tags, err := list[Ref](ctx, c, "/api/extras/tags/", url.Values{"slug": {slug}})
	if err != nil || len(tags) == 0 {
		return 0, err
	}
	return tags[0].ID, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
