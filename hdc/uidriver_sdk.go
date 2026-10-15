package hdc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/uiautodev/go-hdckit/uitestkit_sdk"
)

var (
	embeddedSDKOnce sync.Once
	embeddedSDKPath string
	embeddedSDKErr  error
)

// embeddedSDK extracts the embedded agent library to a temporary file.
func embeddedSDK() (string, error) {
	embeddedSDKOnce.Do(func() {
		name := "uitest_agent_v" + uitestkit_sdk.Version + ".so"
		embeddedSDKPath = filepath.Join(os.TempDir(), name)
		embeddedSDKErr = os.WriteFile(embeddedSDKPath, uitestkit_sdk.Agent, 0o644)
	})
	return embeddedSDKPath, embeddedSDKErr
}

// sdkVersionOf extracts the "x.y.z" version that follows the "@v" marker in the
// agent library header.
func sdkVersionOf(raw string) string {
	idx := strings.Index(raw, "@v")
	if idx < 0 {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(raw[idx+2:]))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// cmpVersion compares two dotted numeric versions, returning -1, 0 or 1.
func cmpVersion(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := max(len(as), len(bs))
	for i := 0; i < n; i++ {
		ai, bi := 0, 0
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}
