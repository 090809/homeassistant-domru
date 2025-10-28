package responder

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

func Read(resp *http.Response) ([]byte, error) {
	var reader io.Reader = resp.Body
	// Проверяем, сжат ли ответ gzip
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gzipReader.Close()
		reader = gzipReader
	}

	return io.ReadAll(reader)
}
