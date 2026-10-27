package main

import (
	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/adapter/claudecode"
)

// agent is the agent stele hook answers and stele init installs for. This is
// the only file in cmd/stele that names one (ADR-0009). v1 has one; a second
// adds a way to choose, a flag on the command init installs.
var agent adapter.Adapter = claudecode.Adapter{}
