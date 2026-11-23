scope: ["internal/**/*.go", "!**/*_test.go"]
check: regex
pattern: \bfmt\.Print
message: log through internal/log (log.Info), not fmt.Print

Code under internal/ never prints with fmt.Print, Printf or Println. Write
output through the shared logger: example.com/x/internal/log, log.Info(msg,
key, value...).
