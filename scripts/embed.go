// Package scripts embeds the dependency-free terminal connection helper.
package scripts

import _ "embed"

//go:embed pocket-connect.py
var MCPConnect []byte
