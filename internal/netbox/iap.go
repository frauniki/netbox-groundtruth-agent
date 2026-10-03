//go:build iap

package netbox

import (
	"errors"
	"net/http"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/credentials/idtoken"

	"github.com/frauniki/netbox-groundtruth-agent/internal/config"
)

// iapTransport sends a Google-signed ID token for the IAP audience in
// Proxy-Authorization, so the NetBox token in Authorization reaches NetBox
// untouched. See https://cloud.google.com/iap/docs/authentication-howto.
func iapTransport(next http.RoundTripper, cfg config.IAP) (http.RoundTripper, error) {
	if cfg.Audience == "" {
		return nil, errors.New("netbox.iap.audience is not set")
	}
	opts := &idtoken.Options{Audience: cfg.Audience}
	var creds *auth.Credentials
	var err error
	if cfg.CredentialsFile != "" {
		creds, err = idtoken.NewCredentialsFromFile(credentials.ServiceAccount, cfg.CredentialsFile, opts)
	} else {
		creds, err = idtoken.NewCredentials(opts) // Application Default Credentials
	}
	if err != nil {
		return nil, err
	}
	return iapRoundTripper{next, creds}, nil
}

type iapRoundTripper struct {
	next  http.RoundTripper
	creds *auth.Credentials
}

func (t iapRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	tok, err := t.creds.Token(r.Context())
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	r.Header.Set("Proxy-Authorization", "Bearer "+tok.Value)
	return t.next.RoundTrip(r)
}
