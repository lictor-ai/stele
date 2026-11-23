# Project rules

Follow these rules for every change. Rules are separated by `---`; a `scope:` line lists the files a rule applies to (none means every file).

scope: migrations/

Every migration in migrations/ has been merged and applied in production.
Never edit or rename one. To change the schema, add a new migration with the
next number, such as 002_<what>.sql.
