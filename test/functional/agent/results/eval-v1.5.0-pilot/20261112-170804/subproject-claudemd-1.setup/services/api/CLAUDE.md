# Project rules

Follow these rules for every change. Rules are separated by `---`; a `scope:` line lists the files a rule applies to (none means every file).

scope: ["**/*.go"]
check: regex
pattern: context\.(Background|TODO)\(\)
message: use the request's context (r.Context()), not context.Background

Handlers and the code they call use the request's context, r.Context(), so a
cancelled request stops its work. Never call context.Background() or
context.TODO() in this service.
