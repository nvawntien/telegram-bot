// Package telegram owns Telegram Bot API transport and update presentation.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const defaultResponseBodyLimit int64 = 1 << 20

type Button struct {
	Text string
	Data string
	URL  string
}

type Keyboard [][]Button

type SendMessageRequest struct {
	ChatID   int64
	Text     string
	Keyboard Keyboard
}

type EditMessageRequest struct {
	ChatID    int64
	MessageID int
	Text      string
	Keyboard  Keyboard
}

type AnswerCallbackRequest struct {
	CallbackID string
	Text       string
	ShowAlert  bool
}

type Messenger interface {
	SendMessage(context.Context, SendMessageRequest) error
	EditMessage(context.Context, EditMessageRequest) error
	AnswerCallback(context.Context, AnswerCallbackRequest) error
}

type APIMetrics interface {
	ObserveTelegramAPI(method, result string, duration time.Duration)
}

type APIError struct {
	Kind       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("Telegram API %s; retry after %s", e.Kind, e.RetryAfter)
	}
	return "Telegram API " + e.Kind
}

type Client struct {
	bot           *telegrambot.Bot
	metrics       APIMetrics
	token         string
	serverURL     string
	deliveryHTTP  *http.Client
	responseLimit int64
}

func NewClient(token, serverURL string, timeout time.Duration, responseLimit int64, metrics APIMetrics) (*Client, error) {
	if responseLimit <= 0 {
		responseLimit = defaultResponseBodyLimit
	}
	baseHTTPClient := &http.Client{Timeout: timeout}
	httpClient := &limitedHTTPClient{
		client: baseHTTPClient,
		limit:  responseLimit,
	}
	options := []telegrambot.Option{
		telegrambot.WithSkipGetMe(),
		telegrambot.WithHTTPClient(timeout, httpClient),
	}
	if serverURL != "" {
		options = append(options, telegrambot.WithServerURL(serverURL))
	}
	bot, err := telegrambot.New(token, options...)
	if err != nil {
		return nil, errors.New("initialize Telegram API client")
	}
	if serverURL == "" {
		serverURL = "https://api.telegram.org"
	}
	return &Client{
		bot: bot, metrics: metrics, token: token, serverURL: strings.TrimRight(serverURL, "/"),
		deliveryHTTP: baseHTTPClient, responseLimit: responseLimit,
	}, nil
}

func (c *Client) SendMessage(ctx context.Context, request SendMessageRequest) error {
	return c.call("sendMessage", func() error {
		_, err := c.bot.SendMessage(ctx, &telegrambot.SendMessageParams{
			ChatID: request.ChatID, Text: request.Text,
			ParseMode: models.ParseModeHTML, ReplyMarkup: mapKeyboard(request.Keyboard),
		})
		return err
	})
}

func (c *Client) EditMessage(ctx context.Context, request EditMessageRequest) error {
	return c.call("editMessageText", func() error {
		_, err := c.bot.EditMessageText(ctx, &telegrambot.EditMessageTextParams{
			ChatID: request.ChatID, MessageID: request.MessageID, Text: request.Text,
			ParseMode: models.ParseModeHTML, ReplyMarkup: mapKeyboard(request.Keyboard),
		})
		return err
	})
}

func (c *Client) AnswerCallback(ctx context.Context, request AnswerCallbackRequest) error {
	return c.call("answerCallbackQuery", func() error {
		_, err := c.bot.AnswerCallbackQuery(ctx, &telegrambot.AnswerCallbackQueryParams{
			CallbackQueryID: request.CallbackID, Text: request.Text, ShowAlert: request.ShowAlert,
		})
		return err
	})
}

func (c *Client) call(method string, call func() error) error {
	started := time.Now()
	err := call()
	result := "success"
	if err != nil {
		result = classifyAPIError(err).Kind
	}
	if c.metrics != nil {
		c.metrics.ObserveTelegramAPI(method, result, time.Since(started))
	}
	if err != nil {
		return classifyAPIError(err)
	}
	return nil
}

func classifyAPIError(err error) *APIError {
	var rateLimit *telegrambot.TooManyRequestsError
	switch {
	case errors.As(err, &rateLimit):
		return &APIError{Kind: "rate_limited", RetryAfter: time.Duration(rateLimit.RetryAfter) * time.Second}
	case errors.Is(err, context.DeadlineExceeded):
		return &APIError{Kind: "timeout"}
	case errors.Is(err, context.Canceled):
		return &APIError{Kind: "cancelled"}
	case errors.Is(err, errResponseTooLarge):
		return &APIError{Kind: "response_too_large"}
	case errors.Is(err, telegrambot.ErrorUnauthorized):
		return &APIError{Kind: "unauthorized"}
	case errors.Is(err, telegrambot.ErrorForbidden):
		return &APIError{Kind: "forbidden"}
	case errors.Is(err, telegrambot.ErrorBadRequest):
		return &APIError{Kind: "bad_request"}
	default:
		return &APIError{Kind: "invalid_response"}
	}
}

func mapKeyboard(keyboard Keyboard) models.ReplyMarkup {
	if len(keyboard) == 0 {
		return nil
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(keyboard))
	for _, row := range keyboard {
		buttons := make([]models.InlineKeyboardButton, 0, len(row))
		for _, button := range row {
			buttons = append(buttons, models.InlineKeyboardButton{
				Text: button.Text, CallbackData: button.Data, URL: button.URL,
			})
		}
		rows = append(rows, buttons)
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

var errResponseTooLarge = errors.New("telegram API response exceeds limit")

type limitedHTTPClient struct {
	client *http.Client
	limit  int64
}

func (c *limitedHTTPClient) Do(request *http.Request) (*http.Response, error) {
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > c.limit {
		_ = response.Body.Close()
		return nil, errResponseTooLarge
	}
	response.Body = &limitedReadCloser{
		Reader: io.LimitReader(response.Body, c.limit+1),
		closer: response.Body,
		limit:  c.limit,
	}
	return response, nil
}

type limitedReadCloser struct {
	io.Reader
	closer io.Closer
	limit  int64
	read   int64
}

func (r *limitedReadCloser) Read(buffer []byte) (int, error) {
	count, err := r.Reader.Read(buffer)
	r.read += int64(count)
	if r.read > r.limit {
		return count, errResponseTooLarge
	}
	return count, err
}

func (r *limitedReadCloser) Close() error {
	return r.closer.Close()
}
