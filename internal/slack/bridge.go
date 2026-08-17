package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/0xdeafcafe/gack/internal/gack"
)

type HTTPBridge struct {
	URL        string
	Token      string
	HTTPClient *http.Client
}

func (b *HTTPBridge) Interact(ctx context.Context, interaction gack.Interaction) (gack.InteractionResult, error) {
	if strings.TrimSpace(b.URL) == "" {
		return gack.InteractionResult{}, fmt.Errorf("interaction bridge URL is empty")
	}
	client := b.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	body, err := json.Marshal(interaction)
	if err != nil {
		return gack.InteractionResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL, bytes.NewReader(body))
	if err != nil {
		return gack.InteractionResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "gack/0.1")
	if b.Token != "" {
		request.Header.Set("Authorization", "Bearer "+b.Token)
	}
	response, err := client.Do(request)
	if err != nil {
		return gack.InteractionResult{}, fmt.Errorf("interaction bridge: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return gack.InteractionResult{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return gack.InteractionResult{}, fmt.Errorf("interaction bridge: HTTP %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	if len(data) == 0 {
		return gack.InteractionResult{}, nil
	}
	var result gack.InteractionResult
	if err := json.Unmarshal(data, &result); err != nil {
		return gack.InteractionResult{}, fmt.Errorf("interaction bridge response: %w", err)
	}
	return result, nil
}
