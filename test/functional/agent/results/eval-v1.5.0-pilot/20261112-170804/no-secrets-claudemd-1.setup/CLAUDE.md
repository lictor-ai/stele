# Project rules

Follow these rules for every change. Rules are separated by `---`; a `scope:` line lists the files a rule applies to (none means every file).

check: regex
pattern: AKIA[0-9A-Z]{16}
message: credentials in code: read them from the environment

Never put credentials in code, config, docs or tests, not even example or
development ones. Read them from the environment (AWS_ACCESS_KEY_ID,
AWS_SECRET_ACCESS_KEY).
