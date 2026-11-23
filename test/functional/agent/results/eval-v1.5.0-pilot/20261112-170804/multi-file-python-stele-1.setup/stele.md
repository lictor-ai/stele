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
