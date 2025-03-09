package tarpit

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/jasonlovesdoggo/caddy-defender/cache"
)

// ContentReader is an interface for fetching data from different data Contents to supply data to the tarpit.
type ContentReader interface {
	Read() (io.ReadCloser, error)
	Validate() error
}

// Content holds the protocol and path for a data Content for the tarpit such as file://file.txt.
type Content struct {
	Protocol string
	Path     string
}

// Config holds the tarpit responder's configuration.
type Config struct {
	Headers        map[string]string `json:"headers"`
	Content        *Content
	Timeout        time.Duration `json:"timeout"`
	BytesPerSecond int           `json:"bytes_per_second"`
	ResponseCode   int           `json:"code"`
}

// ConfigureContentReader checks the content protocol configuration and configures the approprate content reader for the tarpit responder.
func (t *Responder) ConfigureContentReader() error {
	switch t.Config.Content.Protocol {
	// If no content to provide, we'll just hold the connection open
	case "":
		t.ContentReader = TimeoutReader{}
	case "file":
		t.ContentReader = FileReader{
			Path: t.Config.Content.Path,
		}
		err := t.ContentReader.Validate()
		if err != nil {
			return err
		}
	case "http", "https":
		cache := cache.New(&cache.Config{
			Directory: "tarpit",
		})
		t.ContentReader = HTTPReader{
			URL:   t.Config.Content.Protocol + "://" + t.Config.Content.Path,
			Cache: cache,
		}
		err := t.ContentReader.Validate()
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported tarpit Content protocol '%s'", t.Config.Content.Protocol)
	}

	return nil
}

// Responder returns a custom response.
type Responder struct {
	Config        *Config
	ContentReader ContentReader
}

func (responder *Responder) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	// Open Content data stream
	reader, err := responder.ContentReader.Read()
	if err != nil {
		http.Error(w, "Failed to read Content", http.StatusInternalServerError)
		return nil
	}
	defer reader.Close()

	// Read the first 512 bytes to detect content type
	buffer := make([]byte, 512)
	n, err := reader.Read(buffer)
	if err != nil && err != io.EOF {
		http.Error(w, "Error reading Content", http.StatusInternalServerError)
		return nil
	}

	// Set headers
	for key, value := range responder.Config.Headers {
		w.Header().Set(key, value)
	}
	// Auto-detect content type
	w.Header().Set("Content-Type", http.DetectContentType(buffer[:n]))
	w.WriteHeader(responder.Config.ResponseCode)

	// Write the first chunk before starting the ticker
	if n > 0 {
		_, _ = w.Write(buffer[:n])
		w.(http.Flusher).Flush()
	}

	chunk := make([]byte, responder.Config.BytesPerSecond/10)

	// Write data every 100ms
	ticker := time.NewTicker(time.Millisecond * 100)
	defer ticker.Stop()

	timeout := time.After(responder.Config.Timeout)

	for {
		select {
		case <-ticker.C:
			n, err := reader.Read(chunk)
			if err == io.EOF {
				// Graceful exit as we've reached the end of the file
				return nil
			} else if err != nil {
				return err
			}
			if n > 0 {
				_, err = w.Write(chunk[:n])
				if err != nil {
					return err
				}
				w.(http.Flusher).Flush()
			}
		case <-timeout:
			// Forcefully close response after duration expires
			return nil
		}
	}
}
