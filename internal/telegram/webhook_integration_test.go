package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type webhookInfoResponse struct {
	OK          bool        `json:"ok"`
	Description string      `json:"description"`
	Result      webhookInfo `json:"result"`
}

type webhookInfo struct {
	URL                          string `json:"url"`
	HasCustomCertificate         bool   `json:"has_custom_certificate"`
	PendingUpdateCount           int    `json:"pending_update_count"`
	LastErrorDate                int64  `json:"last_error_date"`
	LastErrorMessage             string `json:"last_error_message"`
	LastSynchronizationErrorDate int64  `json:"last_synchronization_error_date"`
	MaxConnections               int    `json:"max_connections"`
	IPAddress                    string `json:"ip_address"`
}

func TestTelegramWebhookInfoViaCurl(t *testing.T) {
	if testing.Short() {
		t.Skip("интеграционный тест пропущен в режиме -short")
	}

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		t.Skip("TELEGRAM_BOT_TOKEN не задан: пропускаем проверку живого Telegram API")
	}

	curlPath, err := exec.LookPath("curl.exe")
	if err != nil {
		t.Skipf("curl.exe не найден в PATH: %v", err)
	}

	baseURL := os.Getenv("TELEGRAM_API_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	url := fmt.Sprintf("%s/bot%s/getWebhookInfo", baseURL, token)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, curlPath, "-sS", url)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("curl.exe не ответил за 10s")
	}
	if err != nil {
		t.Fatalf("curl.exe завершился ошибкой: %v, вывод: %s", err, string(output))
	}

	var resp webhookInfoResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		t.Fatalf("не удалось распарсить ответ Telegram: %v, тело: %s", err, string(output))
	}
	if !resp.OK {
		t.Fatalf("Telegram API вернул ok=false: %s", string(output))
	}
	if resp.Result.URL == "" {
		t.Fatalf("webhook URL пуст: %s", string(output))
	}

	if expectedURL := os.Getenv("EXPECTED_TELEGRAM_WEBHOOK_URL"); expectedURL != "" && resp.Result.URL != expectedURL {
		t.Fatalf("webhook URL не совпадает: ожидаем %s, получили %s", expectedURL, resp.Result.URL)
	}

	if resp.Result.LastErrorMessage != "" {
		t.Fatalf("webhook имеет last_error_message: %s", resp.Result.LastErrorMessage)
	}
	if resp.Result.LastErrorDate != 0 {
		t.Fatalf("webhook имеет last_error_date: %d", resp.Result.LastErrorDate)
	}
	if resp.Result.LastSynchronizationErrorDate != 0 {
		t.Fatalf("webhook имеет last_synchronization_error_date: %d", resp.Result.LastSynchronizationErrorDate)
	}
}
