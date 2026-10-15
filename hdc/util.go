package hdc

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Forward describes a single TCP forward (or reverse) mapping.
type Forward struct {
	// Target is the connect key of the device the mapping belongs to.
	Target string
	// Local is the host side endpoint, e.g. "tcp:9000".
	Local string
	// Remote is the device side endpoint, e.g. "tcp:8000".
	Remote string
}

var (
	reParam = regexp.MustCompile(`(?m)^[ \t]*(.*?) = (.*?)\r?$`)
)

// parseTargets parses the output of `list targets`.
func parseTargets(s string) []string {
	if strings.Contains(s, "Empty") {
		return []string{}
	}
	out := make([]string, 0)
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// parsePorts parses the output of `fport ls`. When reverse is true the second
// and third columns are swapped (remote, local) as reported by hdc.
func parsePorts(s string, reverse bool) []Forward {
	if strings.Contains(s, "Empty") {
		return []Forward{}
	}
	keyword := "Forward"
	if reverse {
		keyword = "Reverse"
	}
	out := make([]Forward, 0)
	for _, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, keyword) {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		if reverse {
			out = append(out, Forward{Target: parts[0], Local: parts[2], Remote: parts[1]})
		} else {
			out = append(out, Forward{Target: parts[0], Local: parts[1], Remote: parts[2]})
		}
	}
	return out
}

// parseParameters parses the `shell param get` output into key/value pairs.
func parseParameters(s string) map[string]string {
	params := make(map[string]string)
	for _, m := range reParam.FindAllStringSubmatch(s, -1) {
		params[m[1]] = m[2]
	}
	return params
}

// parseTCPPort parses the port from a "tcp:PORT" endpoint.
func parseTCPPort(local string) (int, error) {
	port, err := strconv.Atoi(strings.TrimPrefix(local, "tcp:"))
	if err != nil {
		return 0, err
	}
	return port, nil
}

// freePort returns an available local TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// lastServerPid reads the pid of the last started hdc server from its pid file.
func lastServerPid() int {
	data, err := os.ReadFile(filepath.Join(os.TempDir(), ".HDCServer.pid"))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}
