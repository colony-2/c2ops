// Test-only GCS service. No emulator dependency enters the production module.
package main

import (
	"fmt"
	"io"
	"net/http/httptest"
	"os"

	"github.com/fsouza/fake-gcs-server/fakestorage"
)

func main() {
	s, err := fakestorage.NewServerWithOptions(fakestorage.Options{NoListener: true, Scheme: "http", ExternalURL: "https://storage.googleapis.com", Writer: io.Discard})
	if err != nil {
		panic(err)
	}
	defer s.Stop()
	s.CreateBucket("fixture-bucket")
	h := httptest.NewServer(s.HTTPHandler())
	defer h.Close()
	fmt.Println(h.URL)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
