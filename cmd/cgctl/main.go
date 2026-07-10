package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func endpointFor(arguments []string) (string, error) {
	if len(arguments) == 0 {
		return "", fmt.Errorf("command is required: engines, clusters, topology <cluster-id>, health <cluster-id>")
	}
	switch arguments[0] {
	case "engines":
		return "/api/v1/engines", nil
	case "clusters":
		return "/api/v1/clusters", nil
	case "topology", "health":
		if len(arguments) != 2 || strings.TrimSpace(arguments[1]) == "" {
			return "", fmt.Errorf("%s requires a platform cluster UUID", arguments[0])
		}
		return "/api/v1/clusters/" + url.PathEscape(arguments[1]) + "/" + arguments[0], nil
	default:
		return "", fmt.Errorf("unknown command %q", arguments[0])
	}
}

func main() {
	serverURL := flag.String("server", "http://127.0.0.1:8088", "ClusterGuard HA API URL")
	pretty := flag.Bool("json", false, "format JSON with indentation")
	flag.Parse()
	path, err := endpointFor(flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "cgctl:", err)
		os.Exit(2)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Get(strings.TrimRight(*serverURL, "/") + path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cgctl:", err)
		os.Exit(1)
	}
	defer response.Body.Close()
	var body interface{}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		fmt.Fprintln(os.Stderr, "cgctl: invalid API response:", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	if *pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(body); err != nil {
		fmt.Fprintln(os.Stderr, "cgctl:", err)
		os.Exit(1)
	}
	if response.StatusCode >= 400 {
		os.Exit(1)
	}
}
