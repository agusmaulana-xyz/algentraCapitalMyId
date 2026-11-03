package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultServerDirectoryURL = "https://mt5.mtapi.io/Search"

var serverAccessCache sync.Map

type directoryCompany struct {
	Results []struct {
		Name   string   `json:"name"`
		Access []string `json:"access"`
	} `json:"results"`
}

// lookupServerAccess returns the broker access points (host:port) published for
// the exact MT5 server name. An empty result means the server is not listed.
func lookupServerAccess(server string) []string {
	key := strings.ToLower(strings.TrimSpace(server))
	if key == "" {
		return nil
	}
	if cached, ok := serverAccessCache.Load(key); ok {
		return cached.([]string)
	}
	endpoint := strings.TrimSpace(os.Getenv("MT5_SERVER_DIRECTORY_URL"))
	if endpoint == "" {
		endpoint = defaultServerDirectoryURL
	}
	target, err := url.Parse(endpoint)
	if err != nil || target.Scheme != "https" || target.Host == "" {
		return nil
	}
	values := target.Query()
	values.Set("company", strings.TrimSpace(server))
	target.RawQuery = values.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	// The directory answers very slowly (~18s) to requests without a User-Agent.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AlgentraWorker/1.0)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var companies []directoryCompany
	if json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 2<<20)).Decode(&companies) != nil {
		return nil
	}
	var access []string
	for _, company := range companies {
		for _, entry := range company.Results {
			if strings.EqualFold(strings.TrimSpace(entry.Name), strings.TrimSpace(server)) {
				access = append(access, entry.Access...)
			}
		}
	}
	serverAccessCache.Store(key, access)
	return access
}

// reachableAccessPoint picks the first access point that accepts a TCP
// connection. Private/loopback addresses published by some brokers are skipped.
func reachableAccessPoint(points []string) string {
	for _, point := range points {
		host, _, err := net.SplitHostPort(strings.TrimSpace(point))
		if err != nil || strings.ContainsAny(point, "\r\n,= ") {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified()) {
			continue
		}
		conn, err := net.DialTimeout("tcp", strings.TrimSpace(point), 3*time.Second)
		if err != nil {
			continue
		}
		_ = conn.Close()
		return strings.TrimSpace(point)
	}
	return ""
}

// loginServerFor returns what to put in the terminal's Server= line. A server
// already known to some terminal on this machine is referenced by name; a brand
// new broker is reached through its published access point, after which MT5
// adds the server to its own catalog and the name works from then on.
func (w *worker) loginServerFor(accountDir, server string) string {
	server = strings.TrimSpace(server)
	if dirExists(filepath.Join(accountDir, "bases", server)) {
		return server
	}
	if terminalCatalogWithServer(server, w.cfg.masterTerminalPath) != "" {
		return server
	}
	if point := reachableAccessPoint(lookupServerAccess(server)); point != "" {
		return point
	}
	return server
}
