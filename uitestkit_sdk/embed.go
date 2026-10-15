// Package uitestkit_sdk embeds the OpenHarmony uitest agent used by UiDriver.
package uitestkit_sdk

import _ "embed"

// Version is the version of the embedded uitest agent.
const Version = "1.1.0"

// Agent is the embedded uitest agent shared library, pushed to the device as
// /data/local/tmp/agent.so when it is missing or out of date.
//
//go:embed uitest_agent_v1.1.0.so
var Agent []byte
