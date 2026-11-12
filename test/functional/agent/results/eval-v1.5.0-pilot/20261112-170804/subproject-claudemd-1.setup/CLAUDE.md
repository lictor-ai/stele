# Project rules

Follow these rules for every change. Rules are separated by `---`; a `scope:` line lists the files a rule applies to (none means every file).

check: regex
pattern: \bTODO\b
message: no TODO comments: open an issue instead

Never leave TODO notes in code or docs. Open an issue and do the work, or
leave it out.
