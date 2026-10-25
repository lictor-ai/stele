package main

import (
	"net/http"

	"example.com/api/db"
)

var conn = &db.DB{}

func main() {
	_ = conn
	http.ListenAndServe(":8080", nil)
}
