//go:build !iap

package netbox

import (
	"errors"
	"net/http"

	"github.com/frauniki/netbox-groundtruth-agent/internal/config"
)

func iapTransport(http.RoundTripper, config.IAP) (http.RoundTripper, error) {
	return nil, errors.New(`netbox.auth "iap" needs a binary built with the "iap" build tag`)
}
