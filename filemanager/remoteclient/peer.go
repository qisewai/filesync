package fileindex

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Peer endpoints carry a certificate pin, never credentials, in their fragment.
// The pin is exchanged out of band with the invitation and is not sent to HTTP.
func PeerAddress(address, fingerprint string) (string, error) {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return "", err
	}
	pin, err := hex.DecodeString(fingerprint)
	if err != nil || len(pin) != sha256.Size {
		return "", errors.New("invalid device fingerprint")
	}
	u := &url.URL{Scheme: "https", Host: address, Fragment: strings.ToLower(fingerprint)}
	return u.String(), nil
}

func IsPeer(address string) bool { return strings.HasPrefix(address, "https://") }

func peerHTTP(address string) (*http.Client, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Path != "" {
		return nil, errors.New("invalid peer address")
	}
	normalized, err := PeerAddress(u.Host, u.Fragment)
	if err != nil || normalized != address {
		return nil, errors.New("invalid peer address or certificate pin")
	}
	tr := transport.Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, // trust the explicitly exchanged pin, not a public CA
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("missing device certificate")
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			if hex.EncodeToString(sum[:]) != u.Fragment {
				return errors.New("device certificate changed; pair again")
			}
			return nil
		},
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
