package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type mt5DirectoryCompany struct {
	CompanyName string `json:"companyName"`
	Results     []struct {
		Name string `json:"name"`
	} `json:"results"`
}

func mt5ServerDirectory(endpoint string) http.HandlerFunc {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = "https://mt5.mtapi.io/Search"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if len([]rune(query)) < 3 || len([]rune(query)) > 96 {
			writeJSON(w, http.StatusOK, map[string]any{"servers": []any{}})
			return
		}
		target, err := url.Parse(endpoint)
		if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.Fragment != "" {
			writeError(w, http.StatusInternalServerError, "Direktori broker tidak dikonfigurasi dengan aman.")
			return
		}
		values := target.Query()
		values.Set("company", query)
		target.RawQuery = values.Encode()
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
		if err != nil {
			writeError(w, http.StatusBadGateway, "Pencarian direktori broker gagal.")
			return
		}
		req.Header.Set("Accept", "application/json")
		// The directory is very slow for requests without a User-Agent.
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AlgentraBackend/1.0)")
		client := http.Client{Timeout: 20 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			writeError(w, http.StatusBadGateway, "Pencarian direktori broker gagal.")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			writeError(w, http.StatusBadGateway, "Pencarian direktori broker sedang tidak tersedia.")
			return
		}
		var companies []mt5DirectoryCompany
		if err := json.NewDecoder(http.MaxBytesReader(w, resp.Body, 2<<20)).Decode(&companies); err != nil {
			writeError(w, http.StatusBadGateway, "Respons direktori broker tidak valid.")
			return
		}
		type server struct {
			Name    string `json:"name"`
			Company string `json:"company"`
		}
		seen := map[string]bool{}
		servers := make([]server, 0)
		for _, company := range companies {
			companyName := strings.TrimSpace(company.CompanyName)
			for _, entry := range company.Results {
				name := strings.TrimSpace(entry.Name)
				key := strings.ToLower(companyName) + "|" + strings.ToLower(name)
				if name == "" || companyName == "" || seen[key] {
					continue
				}
				seen[key] = true
				servers = append(servers, server{Name: name, Company: companyName})
			}
		}
		needle := strings.ToLower(query)
		sort.Slice(servers, func(i, j int) bool {
			a, b := strings.ToLower(servers[i].Name), strings.ToLower(servers[j].Name)
			if (a == needle) != (b == needle) {
				return a == needle
			}
			return servers[i].Name < servers[j].Name
		})
		if len(servers) > 200 {
			servers = servers[:200]
		}
		w.Header().Set("Cache-Control", "public, max-age=1800")
		writeJSON(w, http.StatusOK, map[string]any{"servers": servers})
	}
}
