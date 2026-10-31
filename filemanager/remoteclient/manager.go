package fileindex

import (
	"net/http"

	fmclient "filemanager/client"
)

// Manager provides the versioned shared API through a FileIndex installation.
// Authentication remains with the compatibility server. Call Info first before
// enabling any capability: older installations do not provide this endpoint.
func (c *Client) Manager() *fmclient.Client {
	m := fmclient.New(c.url("/api/manager", nil), c.hc)
	m.Headers = http.Header{csrfHeader: []string{csrfValue}}
	if c.Token != "" {
		m.SessionCookie = &http.Cookie{Name: cookieName, Value: c.Token}
	}
	return m
}
