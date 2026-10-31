This package is derived from github.com/grandcat/zeroconf v1.0.0 (MIT;
see LICENSE). It is kept locally to correct Windows IPv4 multicast browsing:
bind to the wildcard address with address reuse and explicitly select each
outgoing multicast interface. Other platforms retain the upstream behavior.
