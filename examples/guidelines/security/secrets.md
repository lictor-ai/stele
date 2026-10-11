---
stele: 1
id: SEC-SECRETS
title: Secrets handling
version: 1.0.0
owner: security-architecture
paths: ["**/*.py", "**/*.ts", "**/*.js"]
severity: high
---

Credentials committed to source code are one of the most common audit
findings. Once pushed, a secret must be treated as compromised and rotated.
All credentials are read at runtime from the secrets vault.

## SEC-001: No hardcoded credentials
Never put passwords, API keys, tokens or private keys in source code, tests
or configuration committed to the repository. Read them at runtime with
`get_secret("<name>")`.

```stele
check: regex
pattern: '(?i)(password|passwd|api_key|apikey|secret|token)\s*[:=]\s*["''][^"'']{8,}["'']'
blocking: true
```

## SEC-002: Never log secret values
Do not log, print or include in error messages any variable that holds a
credential, even at debug level. Log the secret's name if you need to trace
which one was used.

```stele
check: llm
blocking: false
```

## SEC-003: Fail closed when a secret is missing
If a required secret cannot be read, stop with an explicit error. Never fall
back to a default or empty value.
