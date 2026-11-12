# Project rules

Follow these rules for every change. Rules are separated by `---`; a `scope:` line lists the files a rule applies to (none means every file).

scope: ["**/*.py"]
check: regex
pattern: \bprint\(
message: use the logging module, not print

Python code never calls print. Output goes through the logging module:
logger = logging.getLogger(__name__), then logger.info(...).

---

scope: ["**/*.py"]
check: regex
pattern: \bos\.path\b
message: use pathlib, not os.path

Paths are pathlib.Path objects. Never use os.path.
