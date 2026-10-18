package notify

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Ping GETs a heartbeat URL (DESIGN §9): a dead-man's switch service that
// alerts when the pings stop, which Gosha can't do itself once it's down.
func Ping(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("heartbeat: %s", resp.Status)
	}
	return nil
}
